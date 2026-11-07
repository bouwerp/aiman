package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/transcript"
)

// transcriptLRUSize is how many sessions' indexes stay open beyond the pinned
// ones. A session read within transcriptPinTTL is pinned and never evicted.
const transcriptLRUSize = 8

// NewTranscriptSource returns the TranscriptSource cmd/aiman/serve.go passes
// to SetTranscripts. It finds a session's transcript with transcript.Locate
// under home, by the base agent command, AgentSessionID, AgentSessionPath and
// WorkingDirectory, and opens it with that agent's normaliser
// (docs/specs/phone-transcript.md, Structure and Behaviour rules 9 and 10).
//
// A stored AgentSessionPath is reported by a hook inside the agent, so it is
// used only when it resolves inside one of the vendor session roots under
// home; anything else is ErrNoTranscript and the RPCs answer with the pane.
func NewTranscriptSource(home string) TranscriptSource {
	return &fileTranscripts{home: home, now: time.Now, entries: map[string]*trEntry{}}
}

type fileTranscripts struct {
	home string
	now  func() time.Time

	mu      sync.Mutex
	entries map[string]*trEntry // by session id
}

// trEntry is one session: its parent file and the sub-agent files opened so far.
type trEntry struct {
	path string
	kind string
	used time.Time

	parent *trFile

	mu   sync.Mutex
	subs map[string]*trFile
}

// vendorRoots are the session trees under home a stored AgentSessionPath must
// resolve inside, by agent kind.
func vendorRoots(home string) map[string]string {
	return map[string]string{
		"claude": filepath.Join(home, ".claude", "projects"),
		"codex":  filepath.Join(home, ".codex", "sessions"),
		"pi":     filepath.Join(home, ".pi", "agent", "sessions"),
		"grok":   filepath.Join(home, ".grok", "sessions"),
	}
}

func (f *fileTranscripts) Open(sess domain.Session, subagent string) (TranscriptReader, error) {
	kind := agent.BaseOf(sess.AgentName)
	explicit := ""
	if sess.AgentSessionPath != "" {
		root, known := vendorRoots(f.home)[kind]
		resolved, exists, ok := confineOutputPath(sess.AgentSessionPath, []string{root})
		if !known || !ok {
			return nil, fmt.Errorf("%w: session path outside the agent's session directory", transcript.ErrNoTranscript)
		}
		if exists {
			explicit = resolved
		}
	}
	path, kind, err := transcript.Locate(f.home, kind, sess.AgentSessionID, explicit, sess.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	e, err := f.entry(sess.ID, path, kind)
	if err != nil {
		return nil, err
	}
	if subagent == "" {
		return e.parent, nil
	}
	return f.sub(e, subagent)
}

// entry returns the session's entry, rebuilding it when the located file
// moved, and evicts the least recently used unpinned entries over the cap.
func (f *fileTranscripts) entry(id, path, kind string) (*trEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	e := f.entries[id]
	if e == nil || e.path != path || e.kind != kind {
		parent, err := f.newFile(path, kind, f.sessionDirs(path, kind), nil)
		if err != nil {
			return nil, err
		}
		e = &trEntry{path: path, kind: kind, parent: parent, subs: map[string]*trFile{}}
		parent.entry = e
		f.entries[id] = e
	}
	e.used = now
	f.evict(now)
	e.dropIdleFull(now)
	return e, nil
}

func (f *fileTranscripts) evict(now time.Time) {
	for len(f.entries) > transcriptLRUSize {
		oldest := ""
		var at time.Time
		for id, e := range f.entries {
			if now.Sub(e.used) < transcriptPinTTL {
				continue
			}
			if oldest == "" || e.used.Before(at) {
				oldest, at = id, e.used
			}
		}
		if oldest == "" {
			return
		}
		delete(f.entries, oldest)
	}
}

func (e *trEntry) dropIdleFull(now time.Time) {
	e.parent.dropIdleFull(now)
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.subs {
		s.dropIdleFull(now)
	}
}

// sessionDirs are the located session's own directories, where a process
// output path must resolve (Behaviour rule 10).
func (f *fileTranscripts) sessionDirs(path, kind string) []string {
	switch kind {
	case "claude":
		dir := strings.TrimSuffix(path, ".jsonl")
		slug, id := filepath.Base(filepath.Dir(path)), filepath.Base(dir)
		dirs := []string{dir}
		uid := "claude-" + strconv.Itoa(os.Getuid())
		for _, tmp := range uniq("/tmp", os.TempDir()) {
			dirs = append(dirs, filepath.Join(tmp, uid, slug, id))
		}
		return dirs
	case "grok":
		return []string{filepath.Dir(path)}
	default:
		return nil
	}
}

func uniq(xs ...string) []string {
	var out []string
	for _, x := range xs {
		if x != "" && !contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// normaliserFor builds a fresh normaliser for one file. A normaliser with no
// position kind is not built yet, so its agent has no transcript.
func normaliserFor(path, kind string, claudeSessionDir string) (transcript.Normaliser, error) {
	var n transcript.Normaliser
	switch kind {
	case "claude":
		n = transcript.NewClaudeWith(transcript.ClaudeOptions{SubagentMeta: transcript.ClaudeMetaLookup(claudeSessionDir)})
	case "codex":
		n = transcript.NewCodex()
	case "pi":
		n = transcript.NewPi()
	case "grok":
		n = transcript.NewGrokWith(transcript.GrokOptions{SessionDir: filepath.Dir(path)})
	default:
		return nil, fmt.Errorf("%w: agent %q", transcript.ErrNoTranscript, kind)
	}
	if pk := n.PositionKind(); pk != transcript.PositionOffset && pk != transcript.PositionOrdinal {
		return nil, fmt.Errorf("%w: the %s reader is not available", transcript.ErrNoTranscript, kind)
	}
	return n, nil
}

func (f *fileTranscripts) newFile(path, kind string, dirs []string, parentEntry *trEntry) (*trFile, error) {
	claudeDir := strings.TrimSuffix(path, ".jsonl")
	if parentEntry != nil {
		claudeDir = strings.TrimSuffix(parentEntry.path, ".jsonl")
	}
	mk := func() (transcript.Normaliser, error) { return normaliserFor(path, kind, claudeDir) }
	n, err := mk()
	if err != nil {
		return nil, err
	}
	tn := newTrackedNorm(n)
	return &trFile{
		path: path, kind: kind, dirs: dirs, now: f.now, newNorm: mk, norm: tn,
		ix:       transcript.NewIndex(path, tn, transcript.IndexOptions{MaxPageBytes: TranscriptMaxReplyBytes - transcriptReplyMargin}),
		confined: map[string]string{},
		entry:    parentEntry,
		src:      f,
	}, nil
}

// sub opens a sub-agent's file: Claude subagents/agent-<id>.jsonl, a Codex
// child rollout whose head names this parent, or a Grok sibling session.
func (f *fileTranscripts) sub(e *trEntry, id string) (*trFile, error) {
	if !plainName(id) {
		return nil, fmt.Errorf("%w: sub-agent %q", transcript.ErrNoTranscript, id)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if s := e.subs[id]; s != nil {
		return s, nil
	}
	path, dirs, err := f.subPath(e, id)
	if err != nil {
		return nil, err
	}
	s, err := f.newFile(path, e.kind, dirs, e)
	if err != nil {
		return nil, err
	}
	e.subs[id] = s
	return s, nil
}

func (f *fileTranscripts) subPath(e *trEntry, id string) (string, []string, error) {
	missing := fmt.Errorf("%w: sub-agent %q", transcript.ErrNoTranscript, id)
	switch e.kind {
	case "claude":
		p := filepath.Join(strings.TrimSuffix(e.path, ".jsonl"), "subagents", "agent-"+id+".jsonl")
		if !regularFile(p) {
			return "", nil, missing
		}
		return p, e.parent.dirs, nil
	case "codex":
		p, _, err := transcript.Locate(f.home, "codex", id, "", "")
		if err != nil {
			return "", nil, missing
		}
		parent, _, err := transcript.CodexChildHead(p)
		if err != nil || parent == "" || parent != codexThreadID(e.path) {
			return "", nil, missing
		}
		return p, nil, nil
	case "grok":
		sessDir := filepath.Dir(e.path)
		for _, child := range []string{id, grokChildSession(sessDir, id)} {
			if !plainName(child) {
				continue
			}
			dir := filepath.Join(filepath.Dir(sessDir), child)
			if p := filepath.Join(dir, "updates.jsonl"); regularFile(p) {
				return p, []string{dir}, nil
			}
		}
		return "", nil, missing
	default:
		return "", nil, missing
	}
}

// codexThreadID is the thread id at the end of rollout-<ts>-<thread id>.jsonl.
func codexThreadID(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	const uuidLen = 36
	if len(name) < uuidLen {
		return ""
	}
	return name[len(name)-uuidLen:]
}

// grokChildSession reads the child session id from
// <session>/subagents/<id>/meta.json, "" when there is none.
func grokChildSession(sessDir, id string) string {
	raw, err := os.ReadFile(filepath.Join(sessDir, "subagents", id, "meta.json")) //nolint:gosec // G304: id is one plain path element
	if err != nil {
		return ""
	}
	var meta map[string]any
	if json.Unmarshal(raw, &meta) != nil {
		return ""
	}
	for _, k := range []string{"child_session_id", "childSessionId"} {
		if s, ok := meta[k].(string); ok {
			return s
		}
	}
	return ""
}

func plainName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`)
}

func regularFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// trFile is one opened transcript file; it is the TranscriptReader.
type trFile struct {
	src     *fileTranscripts
	entry   *trEntry // the owning session; for a sub-agent, its parent's
	path    string
	kind    string
	dirs    []string
	now     func() time.Time
	newNorm func() (transcript.Normaliser, error)
	norm    *trackedNorm
	ix      *transcript.Index

	mu       sync.Mutex
	confined map[string]string // process id to its confined output path, "" when refused
	full     *transcript.Index // full-size copy for transcript_item, built on demand
	fullNorm *trackedNorm
	fullUsed time.Time
}

func (t *trFile) Page(before *transcript.Cursor, limit int) (transcript.Page, error) {
	p, err := t.ix.Page(before, limit)
	if err == nil {
		t.fillCounts(p.Items)
	}
	return p, err
}

func (t *trFile) After(c transcript.Cursor, limit int) (transcript.Page, error) {
	p, err := t.ix.After(c, limit)
	if err == nil {
		t.fillCounts(p.Items)
		t.fillCounts(p.Changed)
	}
	return p, err
}

// fillCounts sets a Claude sub-agent item's ItemCount from its own file; the
// normaliser leaves it 0. Items share Subagent with the index, so it is copied.
func (t *trFile) fillCounts(items []transcript.Item) {
	if t.kind != "claude" || t.entry == nil || t.src == nil {
		return
	}
	for i := range items {
		sa := items[i].Subagent
		if items[i].Kind != transcript.KindSubagent || sa == nil || sa.ItemCount != 0 {
			continue
		}
		sub, err := t.src.sub(t.entry, sa.ID)
		if err != nil {
			continue
		}
		if _, err := sub.ix.Refresh(); err != nil {
			continue
		}
		c := *sa
		c.ItemCount = sub.norm.count()
		items[i].Subagent = &c
	}
}

// Item looks an id up as an item id, then as a process id. A truncated item
// is read again from a full-size index, so the handler's 1 MiB cap applies to
// the source line, not the 64 KiB copy.
func (t *trFile) Item(id string) (transcript.Item, bool) {
	itemID := id
	it, ok := t.ix.Item(id)
	if !ok {
		if iid, found := t.norm.processItem(id); found {
			itemID = iid
			it, ok = t.ix.Item(iid)
		}
	}
	if !ok || !it.Truncated {
		return it, ok
	}
	if full := t.fullIndex(); full != nil {
		if fi, found := full.Item(itemID); found {
			return fi, true
		}
	}
	return it, true
}

func (t *trFile) fullIndex() *transcript.Index {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.full == nil {
		n, err := t.newNorm()
		if err != nil {
			return nil
		}
		t.fullNorm = newTrackedNorm(n)
		t.full = transcript.NewIndex(t.path, t.fullNorm, transcript.IndexOptions{MaxFieldBytes: TranscriptItemMaxFieldBytes})
	}
	t.fullUsed = t.now()
	if _, err := t.full.Refresh(); err != nil {
		return nil
	}
	return t.full
}

func (t *trFile) dropIdleFull(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.full != nil && now.Sub(t.fullUsed) >= transcriptPinTTL {
		t.full, t.fullNorm = nil, nil
	}
}

func (t *trFile) ProcessPath(id string) (string, bool) {
	return t.norm.processPath(id)
}

func (t *trFile) SessionDirs() []string { return t.dirs }

// Files are this file, for a parent its opened sub-agent files, and the
// confined output files of running processes.
func (t *trFile) Files() []string {
	out := []string{t.path}
	if t.entry != nil && t.entry.parent == t {
		t.entry.mu.Lock()
		for _, s := range t.entry.subs {
			out = append(out, s.path)
		}
		t.entry.mu.Unlock()
	}
	for _, pid := range t.norm.running() {
		if p := t.confinedOutput(pid); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (t *trFile) confinedOutput(pid string) string {
	t.mu.Lock()
	p, ok := t.confined[pid]
	t.mu.Unlock()
	if ok {
		return p
	}
	if raw, found := t.ProcessPath(pid); found {
		if r, _, allowed := confineOutputPath(raw, t.dirs); allowed {
			p = r
		}
	}
	t.mu.Lock()
	t.confined[pid] = p
	t.mu.Unlock()
	return p
}

// trackedNorm wraps a normaliser to watch its deltas: live item ids for a
// sub-agent's item count, and processes by Process.ID. It also serialises the
// process path lookup with Feed, which the index calls from its own goroutine.
type trackedNorm struct {
	inner transcript.Normaliser

	mu        sync.Mutex
	live      map[string]struct{}
	procItem  map[string]string // Process.ID to item id
	itemProcs map[string]string // item id to Process.ID
	isRunning map[string]bool
}

func newTrackedNorm(n transcript.Normaliser) *trackedNorm {
	t := &trackedNorm{inner: n}
	t.clear()
	return t
}

func (t *trackedNorm) clear() {
	t.live = map[string]struct{}{}
	t.procItem = map[string]string{}
	t.itemProcs = map[string]string{}
	t.isRunning = map[string]bool{}
}

func (t *trackedNorm) Kind() string                          { return t.inner.Kind() }
func (t *trackedNorm) Capabilities() transcript.Capabilities { return t.inner.Capabilities() }
func (t *trackedNorm) PositionKind() string                  { return t.inner.PositionKind() }

func (t *trackedNorm) Feed(pos int64, line []byte) []transcript.Delta {
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.inner.Feed(pos, line)
	t.observe(d)
	return d
}

func (t *trackedNorm) Flush() []transcript.Delta {
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.inner.Flush()
	t.observe(d)
	return d
}

func (t *trackedNorm) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inner.Reset()
	t.clear()
}

// observe mirrors the index's delta rules: an Update or Remove for an id that
// is not live changes nothing.
func (t *trackedNorm) observe(deltas []transcript.Delta) {
	for _, d := range deltas {
		switch {
		case d.Add != nil:
			t.live[d.Add.ID] = struct{}{}
			t.noteProcess(d.Add)
		case d.Update != nil:
			if _, ok := t.live[d.Update.ID]; ok {
				t.noteProcess(d.Update)
			}
		case d.Remove != "":
			delete(t.live, d.Remove)
			if pid, ok := t.itemProcs[d.Remove]; ok {
				delete(t.itemProcs, d.Remove)
				if t.procItem[pid] == d.Remove {
					delete(t.procItem, pid)
					delete(t.isRunning, pid)
				}
			}
		}
	}
}

func (t *trackedNorm) noteProcess(it *transcript.Item) {
	if it.Process == nil || it.Process.ID == "" {
		return
	}
	pid := it.Process.ID
	t.procItem[pid] = it.ID
	t.itemProcs[it.ID] = pid
	t.isRunning[pid] = it.Process.State == transcript.ProcessRunning
}

func (t *trackedNorm) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.live)
}

func (t *trackedNorm) processItem(pid string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	id, ok := t.procItem[pid]
	return id, ok
}

func (t *trackedNorm) running() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for pid, r := range t.isRunning {
		if r {
			out = append(out, pid)
		}
	}
	return out
}

func (t *trackedNorm) processPath(id string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch n := t.inner.(type) {
	case transcript.ProcessOutputs:
		return n.ProcessOutput(id)
	case transcript.ProcessPather:
		return n.ProcessPath(id)
	}
	return "", false
}
