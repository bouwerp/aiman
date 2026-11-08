package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// grokMetaMaxBytes caps a sub-agent meta.json read.
	grokMetaMaxBytes = 1 << 20
	// grokChildMaxBytes caps the child file normalised for an item count;
	// a larger child is counted by its newlines instead.
	grokChildMaxBytes = 8 << 20
	// grokChildMaxLine caps one child line; a longer line is skipped.
	grokChildMaxLine = 1 << 20
)

// grokChildKey identifies one version of a child updates.jsonl.
type grokChildKey struct {
	path  string
	size  int64
	mtime int64
}

// subagentSpawned adds a running subagent item. The description and type
// come from the line, else from subagents/<id>/meta.json.
func (g *grokNormaliser) subagentSpawned(l *grokLine) []Delta {
	id := l.upd.SubagentID
	if id == "" {
		return nil
	}
	if _, ok := g.subagents[id]; ok {
		return nil
	}
	at := l.at
	sa := &Subagent{ID: id, Name: l.upd.SubagentType, Description: l.upd.Description, State: SubagentRunning, StartedAt: &at}
	if sa.Name == "" || sa.Description == "" {
		g.applyGrokMeta(sa)
	}
	it := &Item{ID: "subagent:" + id, Kind: KindSubagent, Text: sa.Description, Subagent: sa}
	g.subagents[id] = it
	return g.add(l, it)
}

// subagentFinished ends a subagent item. EndedAt is StartedAt plus
// duration_ms when both are known, so the shown duration is Grok's own.
func (g *grokNormaliser) subagentFinished(l *grokLine) []Delta {
	id := l.upd.SubagentID
	if id == "" {
		return nil
	}
	var out []Delta
	it, ok := g.subagents[id]
	if !ok {
		out = g.subagentSpawned(l)
		it = g.subagents[id]
	}
	sa := it.Subagent
	sa.State = grokSubagentState(l.upd.status())
	end := l.at
	if sa.StartedAt != nil && l.upd.DurationMS > 0 {
		end = sa.StartedAt.Add(time.Duration(l.upd.DurationMS) * time.Millisecond)
	}
	sa.EndedAt = &end
	if n := g.childItemCount(l.upd.ChildSessionID); n > 0 {
		sa.ItemCount = n
	}
	return append(out, g.update(l, it)...)
}

func grokSubagentState(s string) string {
	switch strings.ToLower(s) {
	case "completed", "success", "done":
		return SubagentDone
	case "failed", "error", "cancelled", "canceled", "killed", "timeout", "timed_out":
		return SubagentFailed
	}
	return SubagentUnknown
}

// applyGrokMeta fills a sub-agent's type and description from its
// meta.json under SessionDir. A missing or unreadable file changes nothing.
func (g *grokNormaliser) applyGrokMeta(sa *Subagent) {
	if g.opts.SessionDir == "" || !grokPlainName(sa.ID) {
		return
	}
	raw, err := readRegular(filepath.Join(g.opts.SessionDir, "subagents", sa.ID, "meta.json"), grokMetaMaxBytes)
	if err != nil {
		return
	}
	var meta struct {
		SubagentType string `json:"subagent_type"`
		Description  string `json:"description"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return
	}
	if sa.Name == "" {
		sa.Name = meta.SubagentType
	}
	if sa.Description == "" {
		sa.Description = meta.Description
	}
}

// childItemCount counts the items in the child session's updates.jsonl,
// the sibling folder of SessionDir. It runs when the sub-agent finishes,
// never at spawn, and is cached by path, size and mtime. A missing child
// file is 0.
func (g *grokNormaliser) childItemCount(child string) int {
	if g.opts.SessionDir == "" || !grokPlainName(child) {
		return 0
	}
	path := filepath.Join(filepath.Dir(filepath.Clean(g.opts.SessionDir)), child, "updates.jsonl")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	key := grokChildKey{path: path, size: info.Size(), mtime: info.ModTime().UnixNano()}
	if n, ok := g.childCounts[key]; ok {
		return n
	}
	f, err := os.Open(path) // #nosec G304 -- child is a plain name under SessionDir's parent
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	var n int
	if info.Size() > grokChildMaxBytes {
		n = countNewlines(f)
	} else {
		n = countGrokItems(io.LimitReader(f, grokChildMaxBytes))
	}
	g.childCounts[key] = n
	return n
}

// countGrokItems runs a lookup-free normaliser over r and counts its Adds.
func countGrokItems(r io.Reader) int {
	n := NewGrok()
	var pos int64
	count := 0
	readCappedLines(r, grokChildMaxLine, func(line []byte, end int64) {
		pos = end
		count += countAdds(n.Feed(pos, line))
	})
	return count + countAdds(n.Flush())
}

// readCappedLines calls fn with each complete line of at most maxLine
// bytes and the offset just past it. Longer lines are skipped whole.
func readCappedLines(r io.Reader, maxLine int, fn func(line []byte, end int64)) {
	br := bufio.NewReaderSize(r, maxLine)
	var pos int64
	skipping := false
	for {
		frag, err := br.ReadSlice('\n')
		pos += int64(len(frag))
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			skipping = true
			continue
		case err != nil:
			return
		case skipping:
			skipping = false
			continue
		}
		fn(frag, pos)
	}
}

// countNewlines counts the lines of r.
func countNewlines(r io.Reader) int {
	buf := make([]byte, 64<<10)
	n := 0
	for {
		k, err := r.Read(buf)
		n += bytes.Count(buf[:k], []byte{'\n'})
		if err != nil {
			return n
		}
	}
}

func countAdds(ds []Delta) int {
	n := 0
	for _, d := range ds {
		if d.Add != nil {
			n++
		}
	}
	return n
}

// taskBackgrounded adds a running process and records its output file.
func (g *grokNormaliser) taskBackgrounded(l *grokLine) []Delta {
	id := l.upd.TaskID
	if id == "" {
		return nil
	}
	if l.upd.OutputFile != "" {
		g.outputs[id] = l.upd.OutputFile
	}
	if it, ok := g.processes[id]; ok {
		return g.update(l, it)
	}
	it := &Item{ID: "process:" + id, Kind: KindProcess, Text: l.upd.Command,
		Process: &Process{ID: id, Command: l.upd.Command, State: ProcessRunning, Output: OutputFile}}
	g.processes[id] = it
	out := g.add(l, it)
	if call, ok := g.calls[l.upd.ToolCallIDSnake]; ok && call.ToolCall.ProcessID == "" {
		call.ToolCall.ProcessID = id
		out = append(out, g.update(l, call)...)
	}
	return out
}

// taskCompleted marks a process exited with the snapshot's exit code.
func (g *grokNormaliser) taskCompleted(l *grokLine) []Delta {
	snap := l.upd.TaskSnapshot
	if snap == nil || snap.TaskID == "" {
		return nil
	}
	var out []Delta
	it, ok := g.processes[snap.TaskID]
	if !ok {
		bg := *l
		bg.upd.TaskID, bg.upd.Command, bg.upd.OutputFile = snap.TaskID, snap.Command, snap.OutputFile
		out = g.taskBackgrounded(&bg)
		it = g.processes[snap.TaskID]
	} else if snap.OutputFile != "" {
		g.outputs[snap.TaskID] = snap.OutputFile
	}
	it.Process.State = ProcessExited
	if snap.ExitCode != nil {
		code := int(*snap.ExitCode)
		it.Process.ExitCode = &code
	}
	return append(out, g.update(l, it)...)
}

// grokPlainName reports whether s is one path component.
func grokPlainName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`)
}

// readRegular reads a regular file (not a symlink) of at most max bytes.
func readRegular(path string, maxBytes int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, errors.New("not a small regular file")
	}
	return os.ReadFile(path) // #nosec G304 -- callers pass a path built from plain names
}
