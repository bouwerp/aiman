package server

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/transcript"
)

// codexChildRescan is how long one session's scan of Codex child rollouts is
// reused before the rollout heads are read again.
const codexChildRescan = 30 * time.Second

// codexHeadMax bounds the session_meta line read from a rollout head; it
// holds base_instructions.
const codexHeadMax = 1 << 20

// codexKids is one Codex parent's child rollouts from the last scan.
type codexKids struct {
	mu   sync.Mutex
	at   time.Time
	end  int64 // the parent's newest page position, the rev a new sighting gets
	list []codexKid
}

// codexKid is one child rollout. firstRev is the parent position when the
// child was first listed, changedRev the position when its state last changed.
type codexKid struct {
	path       string
	sa         transcript.Subagent
	firstRev   int64
	changedRev int64
}

func (k *codexKids) cachedList() []codexKid {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.list
}

// codexHeads caches each rollout's parent thread id by path ("" for a parent
// rollout). A head does not change once its first line is complete.
type codexHeads struct {
	mu     sync.Mutex
	parent map[string]string
}

// parentOf returns the rollout's parent thread id, reading the head once.
func (h *codexHeads) parentOf(path string) string {
	h.mu.Lock()
	p, ok := h.parent[path]
	h.mu.Unlock()
	if ok {
		return p
	}
	p, done := codexHeadParent(path)
	if done {
		h.mu.Lock()
		h.parent[path] = p
		h.mu.Unlock()
	}
	return p
}

// retain drops the cached heads of rollouts no longer on disk.
func (h *codexHeads) retain(paths []string) {
	keep := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		keep[p] = struct{}{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for p := range h.parent {
		if _, ok := keep[p]; !ok {
			delete(h.parent, p)
		}
	}
}

// codexHeadParent reads session_meta.payload.parent_thread_id from a
// rollout's first line. done is false while that line is still being written.
func codexHeadParent(path string) (parent string, done bool) {
	f, err := os.Open(path) //nolint:gosec // G304: a rollout globbed under ~/.codex/sessions
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	line, err := bufio.NewReader(io.LimitReader(f, codexHeadMax)).ReadBytes('\n')
	if err != nil {
		return "", len(line) >= codexHeadMax
	}
	var head struct {
		Type    string `json:"type"`
		Payload struct {
			ParentThreadID string `json:"parent_thread_id"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &head) != nil || head.Type != "session_meta" {
		return "", true
	}
	return head.Payload.ParentThreadID, true
}

var codexGlobMeta = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`)

// codexRollouts lists every rollout file under ~/.codex/sessions.
func (f *fileTranscripts) codexRollouts() []string {
	root := filepath.Join(codexGlobMeta.Replace(f.home), ".codex", "sessions")
	paths, err := filepath.Glob(filepath.Join(root, "*", "*", "*", "rollout-*.jsonl"))
	if err != nil {
		return nil
	}
	return paths
}

// codexChildren returns the parent's child rollouts: those whose head names
// the parent's thread id, with state from transcript.CodexChildHead. The scan
// is reused for codexChildRescan.
func (f *fileTranscripts) codexChildren(e *trEntry) []codexKid {
	k := &e.kids
	k.mu.Lock()
	defer k.mu.Unlock()
	now := f.now()
	if !k.at.IsZero() && now.Sub(k.at) < codexChildRescan {
		return k.list
	}
	k.at = now
	thread := codexThreadID(e.path)
	if thread == "" {
		return nil
	}
	prev := make(map[string]codexKid, len(k.list))
	for _, c := range k.list {
		prev[c.sa.ID] = c
	}
	rollouts := f.codexRollouts()
	f.heads.retain(rollouts)
	var out []codexKid
	for _, p := range rollouts {
		if f.heads.parentOf(p) != thread || !regularFile(p) {
			continue
		}
		parent, sa, err := transcript.CodexChildHead(p)
		if err != nil || parent != thread || !plainName(sa.ID) {
			continue
		}
		out = append(out, nextKid(prev[sa.ID], p, sa, k.end))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].path < out[j].path })
	k.list = out
	return out
}

// nextKid carries a child's revs across scans: a new child, or one whose
// state moved, gets the parent's current position.
func nextKid(old codexKid, path string, sa transcript.Subagent, end int64) codexKid {
	if old.sa.ID == "" {
		return codexKid{path: path, sa: sa, firstRev: end, changedRev: end}
	}
	k := codexKid{path: path, sa: sa, firstRev: old.firstRev, changedRev: old.changedRev}
	if old.sa.State != sa.State {
		k.changedRev = end
	}
	return k
}

// scanAt records the parent's newest position and returns its children.
func (f *fileTranscripts) scanAt(e *trEntry, end int64) []codexKid {
	e.kids.mu.Lock()
	e.kids.end = max(e.kids.end, end)
	e.kids.mu.Unlock()
	return f.codexChildren(e)
}

// childItems are the subagent items for the parent's newest page.
func (f *fileTranscripts) childItems(e *trEntry, end int64) []transcript.Item {
	kids := f.scanAt(e, end)
	out := make([]transcript.Item, 0, len(kids))
	for _, k := range kids {
		out = append(out, codexKidItem(k))
	}
	return out
}

// childChanges adds to an after page the children first listed past the
// cursor as new items, and the others whose state moved at or past it as
// changed items (replaced by id, so a repeat is harmless).
func (f *fileTranscripts) childChanges(e *trEntry, p *transcript.Page, cursor int64) {
	for _, k := range f.scanAt(e, p.After.Position) {
		switch {
		case k.firstRev > cursor:
			p.Items = append(p.Items, codexKidItem(k))
		case k.changedRev >= cursor:
			p.Changed = append(p.Changed, codexKidItem(k))
		}
	}
}

func codexKidItem(k codexKid) transcript.Item {
	sa := k.sa
	it := transcript.Item{
		ID: "codex-child-" + sa.ID, Kind: transcript.KindSubagent, Rev: k.changedRev,
		Text: sa.Name, Subagent: &sa,
	}
	if sa.StartedAt != nil {
		it.At = *sa.StartedAt
	}
	return it
}
