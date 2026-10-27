package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// Kind is the transcript family a path belongs to.
type Kind string

const (
	// KindUnknown is a path the tracker does not read.
	KindUnknown Kind = ""
	// KindClaude is a Claude Code JSONL transcript.
	KindClaude Kind = "claude"
	// KindCodex is a Codex rollout JSONL transcript.
	KindCodex Kind = "codex"
)

// ScanInterval is the default gap between transcript scans.
const ScanInterval = 15 * time.Second

// fingerprintHead is the prefix read to identify a transcript.
// Fingerprints only look at the head, so a later append does not change them.
const fingerprintHead = 64 << 10

// Store is the usage cursor and bucket store. *sqlite.Repository satisfies it.
type Store interface {
	UsageCursors(ctx context.Context, sessionID string) ([]domain.UsageCursor, error)
	ApplyUsage(ctx context.Context, cur domain.UsageCursor, recs []domain.UsageRecord) (bool, error)
	UpsertUsageSession(ctx context.Context, m domain.UsageSessionMeta) error
	UsageSessions(ctx context.Context) ([]domain.UsageSessionMeta, error)
	UsageBuckets(ctx context.Context, sessionIDs []string, since time.Time) ([]domain.UsageBucket, error)
}

// SessionLister lists sessions whose transcripts should be scanned.
type SessionLister interface {
	List(ctx context.Context) ([]domain.Session, error)
}

// Tracker follows transcript files from stored cursors.
type Tracker struct {
	store         Store
	sessions      SessionLister
	onChange      func(sessionID string)
	kick          chan struct{}
	contextWindow int
	testCommands  []string
}

// SetTestCommands records usage.test_commands for first-test detection.
// An empty list means DefaultTestCommands.
func (t *Tracker) SetTestCommands(cmds []string) {
	if t == nil {
		return
	}
	t.testCommands = cmds
}

// SetContextWindow records usage.context_window_tokens.
// A non-positive value does not win: ResolveWindow then uses a confirmed
// table row or the fallback.
func (t *Tracker) SetContextWindow(tokens int) {
	if t == nil {
		return
	}
	t.contextWindow = tokens
}

// NewTracker records the stores the tracker reads.
// onChange may be nil. When set, it is called once per session after a scan
// in which some ApplyUsage added tokens.
func NewTracker(store Store, sessions SessionLister, onChange func(sessionID string)) *Tracker {
	return &Tracker{
		store:    store,
		sessions: sessions,
		onChange: onChange,
		kick:     make(chan struct{}, 1),
	}
}

// KindForPath reports claude for a ".jsonl" path under "/.claude/projects/",
// and codex for a ".jsonl" path under "/.codex/sessions/". Anything else,
// including .grok, is KindUnknown and is not given a cursor.
func KindForPath(path string) Kind {
	if !strings.HasSuffix(path, ".jsonl") {
		return KindUnknown
	}
	switch {
	case strings.Contains(path, "/.claude/projects/"):
		return KindClaude
	case strings.Contains(path, "/.codex/sessions/"):
		return KindCodex
	default:
		return KindUnknown
	}
}

// ScanOnce reads each session's transcripts from the stored cursors.
// The returned ids are the sessions where at least one ApplyUsage added tokens.
func (t *Tracker) ScanOnce(ctx context.Context) ([]string, error) {
	sessions, err := t.sessions.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	var changed []string
	for _, s := range sessions {
		if err := ctx.Err(); err != nil {
			return changed, err
		}
		ok, err := t.scanSession(ctx, s)
		if err != nil {
			return changed, err
		}
		if !ok {
			continue
		}
		changed = append(changed, s.ID)
		if t.onChange != nil {
			t.onChange(s.ID)
		}
	}
	return changed, nil
}

// Run scans once, then again on every tick and on Kick, until ctx is cancelled.
func (t *Tracker) Run(ctx context.Context, every time.Duration) {
	t.scanLog(ctx)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.scanLog(ctx)
		case <-t.kick:
			t.scanLog(ctx)
		}
	}
}

// Kick requests a scan. It does not block, and a pending request is enough:
// kicks that arrive while one is waiting are dropped.
func (t *Tracker) Kick() {
	select {
	case t.kick <- struct{}{}:
	default:
	}
}

func (t *Tracker) scanLog(ctx context.Context) {
	if _, err := t.ScanOnce(ctx); err != nil && ctx.Err() == nil {
		log.Printf("usage tracker: %v", err)
	}
}

func (t *Tracker) scanSession(ctx context.Context, s domain.Session) (bool, error) {
	meta := domain.UsageSessionMeta{
		SessionID: s.ID,
		Name:      s.Name,
		Group:     s.Group,
		IssueKey:  s.IssueKey,
		AgentName: s.AgentName,
	}
	if err := t.store.UpsertUsageSession(ctx, meta); err != nil {
		return false, fmt.Errorf("saving usage session %s: %w", s.ID, err)
	}
	cursors, err := t.store.UsageCursors(ctx, s.ID)
	if err != nil {
		return false, fmt.Errorf("listing usage cursors %s: %w", s.ID, err)
	}
	changed := false
	var occupancy contextSample
	haveOccupancy := false
	clearOccupancy := false
	worktree := s.WorktreePath
	if worktree == "" {
		worktree = s.WorkingDirectory
	}
	prior, _ := t.priorHealth(ctx, s.ID)
	healthAcc := prior
	healthDirty := false
	for _, path := range sessionPaths(s.AgentSessionPath, cursors) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		isMain := path == s.AgentSessionPath
		start := healthAcc
		if !isMain {
			start = newHealthAcc()
		}
		got, err := t.scanFile(ctx, s.ID, path, cursors, worktree, start)
		if err != nil {
			log.Printf("usage tracker: %s: %v", path, err)
			continue
		}
		if got.applied {
			cursors = putCursor(cursors, got.cursor)
		}
		if got.added {
			changed = true
		}
		if !isMain || !got.applied {
			continue
		}
		healthAcc = got.health
		healthDirty = true
		if got.sample.OK {
			occupancy = got.sample
			haveOccupancy = true
			clearOccupancy = false
			continue
		}
		if got.fromZero {
			clearOccupancy = true
			haveOccupancy = false
		}
	}
	if err := t.storeContext(ctx, meta, occupancy, haveOccupancy, clearOccupancy); err != nil {
		return false, err
	}
	if healthDirty {
		if err := t.storeHealth(ctx, meta, healthAcc); err != nil {
			return false, err
		}
	}
	return changed, nil
}

func (t *Tracker) priorHealth(ctx context.Context, sessionID string) (*healthAcc, error) {
	metas, err := t.store.UsageSessions(ctx)
	if err != nil {
		return newHealthAcc(), err
	}
	for _, m := range metas {
		if m.SessionID != sessionID {
			continue
		}
		h := Health{
			Known: m.HealthKnown, ToolCalls: m.HealthToolCalls,
			ReadCalls: m.HealthReadCalls, EditCalls: m.HealthEditCalls,
			EditsWithoutRead: m.HealthEditsWithoutRead,
			FirstTestAt:      m.HealthFirstTestAt, Retries: m.HealthRetries,
		}
		return accFromStored(h, m.HealthReads), nil
	}
	return newHealthAcc(), nil
}

func (t *Tracker) storeHealth(ctx context.Context, meta domain.UsageSessionMeta, acc *healthAcc) error {
	h := acc.finish()
	meta.HealthKnown = h.Known
	meta.HealthToolCalls = h.ToolCalls
	meta.HealthReadCalls = h.ReadCalls
	meta.HealthEditCalls = h.EditCalls
	meta.HealthEditsWithoutRead = h.EditsWithoutRead
	meta.HealthFirstTestAt = h.FirstTestAt
	meta.HealthRetries = h.Retries
	meta.HealthReads = acc.readsJSON()
	meta.HasHealth = true
	if err := t.store.UpsertUsageSession(ctx, meta); err != nil {
		return fmt.Errorf("saving usage health %s: %w", meta.SessionID, err)
	}
	return nil
}

// storeContext writes the main transcript's latest occupancy.
// A label-only upsert leaves a stored turn in place. A full reread with no
// counted turn clears it. Subagent files are not the session window.
func (t *Tracker) storeContext(ctx context.Context, meta domain.UsageSessionMeta, sample contextSample, have, clear bool) error {
	if !have && !clear {
		return nil
	}
	if have {
		window, source := ResolveWindow(sample.Model, t.contextWindow)
		meta.LastInputTokens = sample.Tokens
		meta.ContextWindow = int64(window)
		meta.ContextSource = source
	}
	meta.HasContext = true
	if err := t.store.UpsertUsageSession(ctx, meta); err != nil {
		return fmt.Errorf("saving usage context %s: %w", meta.SessionID, err)
	}
	return nil
}

// sessionPaths is the stored cursor paths, the current agent transcript, and
// Claude subagent transcripts next to each Claude path. Unknown kinds are left out.
func sessionPaths(agentPath string, cursors []domain.UsageCursor) []string {
	set := make(map[string]struct{})
	add := func(path string) {
		if KindForPath(path) == KindUnknown {
			return
		}
		set[path] = struct{}{}
	}
	for _, cur := range cursors {
		add(cur.Path)
	}
	add(agentPath)
	var bases []string
	for path := range set {
		if KindForPath(path) == KindClaude {
			bases = append(bases, path)
		}
	}
	for _, path := range bases {
		for _, match := range claudeSubagents(path) {
			add(match)
		}
	}
	out := make([]string, 0, len(set))
	for path := range set {
		out = append(out, path)
	}
	slices.Sort(out)
	return out
}

func claudeSubagents(path string) []string {
	stem := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	pattern := filepath.Join(filepath.Dir(path), stem, "subagents", "*.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		log.Printf("usage tracker: %s: %v", pattern, err)
		return nil
	}
	return matches
}

// fileScan is one transcript read. applied is false when size and mtime matched
// and the file was not parsed. fromZero is a read that started at the first byte.
type fileScan struct {
	added    bool
	cursor   domain.UsageCursor
	applied  bool
	fromZero bool
	sample   contextSample
	health   *healthAcc
}

// scanFile stats path and skips ApplyUsage when size and mtime are unchanged.
func (t *Tracker) scanFile(ctx context.Context, sessionID, path string, cursors []domain.UsageCursor, worktree string, health *healthAcc) (fileScan, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileScan{}, err
	}
	prev, has := cursorByPath(cursors, path)
	if has && info.Size() == prev.Size && info.ModTime().Equal(prev.ModTime) {
		return fileScan{cursor: prev}, nil
	}
	f, err := os.Open(path) //nolint:gosec // G304: path is a transcript the session or an existing cursor already names
	if err != nil {
		return fileScan{}, err
	}
	defer f.Close()

	kind := KindForPath(path)
	fp, err := readFingerprint(f, kind)
	if err != nil {
		return fileScan{}, err
	}
	offset, reset := resumeOffset(prev, has, info.Size(), fp)
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return fileScan{}, err
	}
	acc := health
	if reset {
		acc = newHealthAcc()
	}
	recs, consumed, state, sample, acc, err := parseTranscript(kind, f, cursors, path, fp, reset, worktree, t.testCommands, acc)
	if err != nil {
		return fileScan{}, err
	}
	cur := domain.UsageCursor{
		SessionID:   sessionID,
		Path:        path,
		Fingerprint: fp,
		Offset:      offset + consumed,
		Size:        info.Size(),
		ModTime:     info.ModTime(),
		State:       state,
	}
	added, err := t.store.ApplyUsage(ctx, cur, recs)
	if err != nil {
		return fileScan{}, err
	}
	return fileScan{added: added, cursor: cur, applied: true, fromZero: offset == 0, sample: sample, health: acc}, nil
}

func resumeOffset(prev domain.UsageCursor, has bool, size int64, fp string) (int64, bool) {
	if !has || size < prev.Offset || fp != prev.Fingerprint {
		return 0, true
	}
	return prev.Offset, false
}

func readFingerprint(f *os.File, kind Kind) (string, error) {
	buf := make([]byte, fingerprintHead)
	n, err := f.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	head := buf[:n]
	if kind == KindCodex {
		return CodexFingerprint(head), nil
	}
	return ClaudeFingerprint(head), nil
}

func parseTranscript(kind Kind, r io.Reader, cursors []domain.UsageCursor, path, fp string, reset bool, worktree string, cmds []string, acc *healthAcc) ([]domain.UsageRecord, int64, string, contextSample, *healthAcc, error) {
	switch kind {
	case KindClaude:
		recs, n, sample, acc, err := parseClaudeHealth(r, worktree, cmds, acc)
		return recs, n, "", sample, acc, err
	case KindCodex:
		st, err := codexStateFor(cursors, path, fp, reset)
		if err != nil {
			return nil, 0, "", contextSample{}, acc, err
		}
		recs, n, sample, acc, err := parseCodexHealth(r, &st, worktree, cmds, acc)
		if err != nil {
			return nil, 0, "", contextSample{}, acc, err
		}
		raw, err := marshalCodexState(st)
		if err != nil {
			return nil, 0, "", contextSample{}, acc, err
		}
		return recs, n, raw, sample, acc, nil
	default:
		return nil, 0, "", contextSample{}, acc, fmt.Errorf("unknown transcript kind %q", kind)
	}
}

// codexStateFor keeps the cursor's own watermark when the file was not reset.
// A reset file (new, truncated, or a new fingerprint) takes the stored state
// with the highest Last total among this session's cursors that share the
// fingerprint, so a continued thread is not counted from zero.
func codexStateFor(cursors []domain.UsageCursor, path, fp string, reset bool) (CodexState, error) {
	if !reset {
		cur, ok := cursorByPath(cursors, path)
		if ok && cur.Fingerprint == fp {
			return decodeCodexState(cur.State)
		}
	}
	return highestCodexState(cursors, fp)
}

// codexStateWire is the cursor JSON. CodexTotals has no tags; the keys are the
// lower-case field names so a stored watermark is not tied to Go's exported spelling.
type codexStateWire struct {
	Model string          `json:"model"`
	Last  codexTotalsWire `json:"last"`
	Seen  int64           `json:"seen"`
}

type codexTotalsWire struct {
	Input      int64 `json:"input"`
	Cached     int64 `json:"cached"`
	CacheWrite int64 `json:"cachewrite"`
	Output     int64 `json:"output"`
}

func marshalCodexState(st CodexState) (string, error) {
	raw, err := json.Marshal(codexStateWire{
		Model: st.Model,
		Seen:  st.Seen,
		Last: codexTotalsWire{
			Input:      st.Last.Input,
			Cached:     st.Last.Cached,
			CacheWrite: st.Last.CacheWrite,
			Output:     st.Last.Output,
		},
	})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func decodeCodexState(raw string) (CodexState, error) {
	if raw == "" {
		return CodexState{}, nil
	}
	var wire codexStateWire
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return CodexState{}, fmt.Errorf("decoding codex state: %w", err)
	}
	return CodexState{
		Model: wire.Model,
		Seen:  wire.Seen,
		Last: CodexTotals{
			Input:      wire.Last.Input,
			Cached:     wire.Last.Cached,
			CacheWrite: wire.Last.CacheWrite,
			Output:     wire.Last.Output,
		},
	}, nil
}

func highestCodexState(cursors []domain.UsageCursor, fp string) (CodexState, error) {
	var best CodexState
	found := false
	for _, cur := range cursors {
		if cur.Fingerprint != fp {
			continue
		}
		st, err := decodeCodexState(cur.State)
		if err != nil {
			return CodexState{}, err
		}
		if !found || laterCodexState(st, best) {
			best = st
			found = true
		}
	}
	return best, nil
}

func laterCodexState(a, b CodexState) bool {
	if a.Last.sum() != b.Last.sum() {
		return a.Last.sum() > b.Last.sum()
	}
	return a.Seen > b.Seen
}

func cursorByPath(cursors []domain.UsageCursor, path string) (domain.UsageCursor, bool) {
	for _, cur := range cursors {
		if cur.Path == path {
			return cur, true
		}
	}
	return domain.UsageCursor{}, false
}

func putCursor(cursors []domain.UsageCursor, cur domain.UsageCursor) []domain.UsageCursor {
	for i := range cursors {
		if cursors[i].Path == cur.Path {
			cursors[i] = cur
			return cursors
		}
	}
	return append(cursors, cur)
}
