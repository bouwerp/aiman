package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/transcript"
	"github.com/bouwerp/aiman/internal/usecase"
)

// CodeForbidden refuses session.process_output when the path the transcript
// names resolves outside the located session's own directory.
const CodeForbidden = "forbidden"

// Transcript reply limits (docs/specs/phone-transcript.md, Behaviour rules 8
// and 10).
const (
	// TranscriptMaxReplyBytes caps the JSON encoding of one session.transcript
	// result; a larger page is shortened and keeps before honest.
	TranscriptMaxReplyBytes = 2 << 20
	// TranscriptItemMaxFieldBytes caps text, input and result in a
	// session.transcript_item reply, and Codex inline process output.
	TranscriptItemMaxFieldBytes = 1 << 20
	// ProcessOutputMaxBytes caps the data of one session.process_output reply.
	ProcessOutputMaxBytes = 256 << 10
	// TranscriptEventDebounce is the shortest gap between two transcript
	// events for one session and file.
	TranscriptEventDebounce = time.Second
)

// TranscriptSource opens a session's transcript. subagent is empty for the
// parent conversation. An error matching transcript.ErrNoTranscript means the
// session has none, and session.transcript answers with the pane.
type TranscriptSource interface {
	Open(sess domain.Session, subagent string) (TranscriptReader, error)
}

// TranscriptReader is one opened transcript, parent or sub-agent.
//
// Page and After follow transcript.Index. Item returns one item by id at full
// size (a process item is keyed by its process id); the handler applies the
// 1 MiB field cap. ProcessPath returns the output file the transcript names
// for a process id, false when the process has no file (Codex inline output
// is then the process item's Text). SessionDirs are the located session's own
// directories that a process output path must resolve inside. Files are the
// files the event poll stats for this reader.
type TranscriptReader interface {
	Page(before *transcript.Cursor, limit int) (transcript.Page, error)
	After(c transcript.Cursor, limit int) (transcript.Page, error)
	Item(id string) (transcript.Item, bool)
	ProcessPath(id string) (string, bool)
	SessionDirs() []string
	Files() []string
}

// SetTranscripts sets where session.transcript, session.transcript_item,
// session.process_output and the transcript event read from. With none set
// the three methods answer unavailable.
func (s *Server) SetTranscripts(src TranscriptSource) {
	s.transcripts = src
}

// transcriptPinTTL is how long a session.transcript call keeps its reader on
// the event poll's watch list.
const transcriptPinTTL = 2 * time.Minute

// transcriptReplyMargin leaves room for the response envelope under
// TranscriptMaxReplyBytes.
const transcriptReplyMargin = 4 << 10

// dispatchTranscript routes the transcript read methods.
func (s *Server) dispatchTranscript(ctx context.Context, req Request) Response {
	switch req.Method {
	case "session.transcript_item":
		return s.handleTranscriptItem(ctx, req)
	case "session.process_output":
		return s.handleProcessOutput(ctx, req)
	default:
		return s.handleTranscript(ctx, req)
	}
}

// resolveTranscriptReq resolves params.session, never params.id, which is an
// item or process id here, and refuses a session caller out of scope.
func (s *Server) resolveTranscriptReq(ctx context.Context, req Request, session string) (domain.Session, Response, bool) {
	if s.transcripts == nil {
		return domain.Session{}, errResp(req.ID, CodeUnavailable, "transcripts are not enabled on this serve"), false
	}
	target := strings.TrimSpace(session)
	if target == "" {
		return domain.Session{}, errResp(req.ID, CodeInvalidParams, "session is required"), false
	}
	list, err := s.loadSessions(ctx)
	if err != nil {
		return domain.Session{}, errResp(req.ID, CodeInvalidParams, err.Error()), false
	}
	sess, ok := resolveSession(list, target)
	if !ok {
		return domain.Session{}, errResp(req.ID, CodeNotFound, "session not found"), false
	}
	if refused, out := s.refuseOutOfScope(ctx, req, sess.ID); out {
		return domain.Session{}, refused, false
	}
	return sess, Response{}, true
}

func (s *Server) handleTranscript(ctx context.Context, req Request) Response {
	var params struct {
		Session  string `json:"session"`
		Subagent string `json:"subagent"`
		Before   string `json:"before"`
		After    string `json:"after"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	sess, fail, ok := s.resolveTranscriptReq(ctx, req, params.Session)
	if !ok {
		return fail
	}
	if params.Before != "" && params.After != "" {
		return errResp(req.ID, CodeInvalidParams, "before and after are exclusive")
	}
	var before, after *transcript.Cursor
	for _, c := range []struct {
		raw string
		dst **transcript.Cursor
	}{{params.Before, &before}, {params.After, &after}} {
		if c.raw == "" {
			continue
		}
		cur, err := transcript.DecodeCursor(c.raw)
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
		*c.dst = &cur
	}
	limit := params.Limit
	if limit <= 0 {
		limit = transcript.DefaultLimit
	}
	limit = min(limit, transcript.MaxLimit)

	reader, err := s.transcripts.Open(sess, params.Subagent)
	if err != nil {
		if errors.Is(err, transcript.ErrNoTranscript) {
			return s.paneTranscript(ctx, req, sess, params.Subagent)
		}
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	page, err := readTranscriptPage(reader, before, after, limit)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s.paneTranscript(ctx, req, sess, params.Subagent)
		}
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	s.pinTranscript(sess.ID, params.Subagent, reader)
	page.Session, page.Subagent = sess.ID, params.Subagent
	if page.Items == nil {
		page.Items = []transcript.Item{}
	}
	return Response{ID: req.ID, Result: page}
}

// readTranscriptPage reads one page and shortens it under
// TranscriptMaxReplyBytes. The reader is asked again with a smaller limit, so
// its own cursors stay right; a reader that still answers too much is cut here.
func readTranscriptPage(r TranscriptReader, before, after *transcript.Cursor, limit int) (transcript.Page, error) {
	read := func(n int) (transcript.Page, error) {
		if after != nil {
			return r.After(*after, n)
		}
		return r.Page(before, n)
	}
	page, err := read(limit)
	if err != nil {
		return page, err
	}
	budget := TranscriptMaxReplyBytes - transcriptReplyMargin
	if pageSize(page) <= budget {
		return page, nil
	}
	if after != nil {
		if fit := fittingEvents(page, budget); fit > 0 && fit < len(page.Items)+len(page.Changed) {
			if again, err := r.After(*after, fit); err == nil && pageSize(again) <= budget {
				return again, nil
			}
		}
		// The reader cannot cut its own reply: start the client over from the
		// newest page rather than lose what lies between.
		page, err = r.Page(nil, limit)
		if err != nil {
			return page, err
		}
		page.Reset = true
	}
	if fit := fittingNewest(page, budget); fit > 0 && fit < len(page.Items) {
		if again, err := r.Page(before, fit); err == nil && pageSize(again) <= budget {
			again.Reset = again.Reset || page.Reset
			return again, nil
		}
	}
	return trimOldest(page, budget), nil
}

func pageSize(p transcript.Page) int {
	b, err := json.Marshal(p)
	if err != nil {
		return 0
	}
	return len(b)
}

func itemSize(it transcript.Item) int {
	b, err := json.Marshal(it)
	if err != nil {
		return 0
	}
	return len(b) + 1
}

// fittingNewest counts the newest items that fit with the page envelope.
func fittingNewest(p transcript.Page, budget int) int {
	env := p
	env.Items = []transcript.Item{}
	room := budget - pageSize(env)
	n := 0
	for i := len(p.Items) - 1; i >= 0; i-- {
		room -= itemSize(p.Items[i])
		if room < 0 {
			break
		}
		n++
	}
	return n
}

// fittingEvents counts the after events, oldest first, that fit.
func fittingEvents(p transcript.Page, budget int) int {
	env := p
	env.Items, env.Changed = []transcript.Item{}, nil
	room := budget - pageSize(env)
	n := 0
	for _, it := range append(append([]transcript.Item{}, p.Items...), p.Changed...) {
		room -= itemSize(it)
		if room < 0 {
			break
		}
		n++
	}
	return n
}

// trimOldest drops the oldest items until the page fits, always keeping the
// newest. before then points at the oldest kept item's rev: paging back from
// it can repeat a kept item but never skips a dropped one.
func trimOldest(p transcript.Page, budget int) transcript.Page {
	keep := max(fittingNewest(p, budget), 1)
	if keep >= len(p.Items) {
		return p
	}
	p.Items = p.Items[len(p.Items)-keep:]
	p.Before = &transcript.Cursor{FileID: p.After.FileID, Kind: p.After.Kind, Position: p.Items[0].Rev}
	return p
}

// paneTranscript answers session.transcript for a session with no transcript:
// one pane item holding the current screen.
func (s *Server) paneTranscript(ctx context.Context, req Request, sess domain.Session, subagent string) Response {
	if sess.IsLegacyTmux() {
		return legacyTerminalResp(req.ID, sess)
	}
	var text string
	switch {
	case s.pty != nil:
		screen, err := s.pty.CaptureScreen(sess.ID)
		if err != nil {
			return errResp(req.ID, CodeUnavailable, err.Error())
		}
		text = screen
	case s.remote != nil:
		raw, err := usecase.CapturePTYPane(ctx, s.remote, sess.ID, 0)
		if err != nil {
			return errResp(req.ID, CodeUnavailable, err.Error())
		}
		text = raw
	}
	text, cut := transcript.Truncate(text, TranscriptItemMaxFieldBytes)
	return Response{ID: req.ID, Result: transcript.Page{
		Type:     transcript.PageType,
		Session:  sess.ID,
		Subagent: subagent,
		Source:   transcript.SourcePane,
		Capabilities: transcript.Capabilities{
			Agent:      agent.BaseOf(sess.AgentName),
			Scrollback: transcript.ScrollbackPane,
			Thinking:   transcript.ThinkingNone,
			Processes:  transcript.ProcessesNone,
		},
		Items: []transcript.Item{{
			ID: "pane", Kind: transcript.KindPane, At: time.Now().UTC(), Text: text, Truncated: cut,
		}},
		After: transcript.Cursor{FileID: "pane", Kind: transcript.PositionOffset},
	}}
}

func (s *Server) handleTranscriptItem(ctx context.Context, req Request) Response {
	var params struct {
		Session  string `json:"session"`
		Subagent string `json:"subagent"`
		ID       string `json:"id"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	sess, fail, ok := s.resolveTranscriptReq(ctx, req, params.Session)
	if !ok {
		return fail
	}
	if params.ID == "" {
		return errResp(req.ID, CodeInvalidParams, "id is required")
	}
	reader, err := s.transcripts.Open(sess, params.Subagent)
	if err != nil {
		return transcriptOpenErr(req.ID, err)
	}
	it, found := reader.Item(params.ID)
	if !found {
		return errResp(req.ID, CodeNotFound, "no item "+params.ID)
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type": "transcript_item",
		"item": capItemFields(it, TranscriptItemMaxFieldBytes),
	}}
}

func transcriptOpenErr(id string, err error) Response {
	if errors.Is(err, transcript.ErrNoTranscript) {
		return errResp(id, CodeNotFound, err.Error())
	}
	return errResp(id, CodeUnavailable, err.Error())
}

// capItemFields cuts text, input and result to max bytes on a copy.
func capItemFields(it transcript.Item, maxBytes int) transcript.Item {
	var cut bool
	it.Text, cut = transcript.Truncate(it.Text, maxBytes)
	it.Truncated = it.Truncated || cut
	if it.ToolCall != nil {
		tc := *it.ToolCall
		var inCut, resCut bool
		tc.Input, inCut = transcript.Truncate(tc.Input, maxBytes)
		tc.Result, resCut = transcript.Truncate(tc.Result, maxBytes)
		it.ToolCall = &tc
		it.Truncated = it.Truncated || inCut || resCut
	}
	return it
}

// processOutputResult is the session.process_output reply.
type processOutputResult struct {
	Type      string `json:"type"`
	Data      string `json:"data"`
	Offset    int64  `json:"offset"`
	Size      int64  `json:"size"`
	State     string `json:"state"`
	ExitCode  *int   `json:"exit_code"`
	Truncated bool   `json:"truncated"`
}

func (s *Server) handleProcessOutput(ctx context.Context, req Request) Response {
	var params struct {
		Session  string `json:"session"`
		Subagent string `json:"subagent"`
		ID       string `json:"id"`
		Since    *int64 `json:"since"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	sess, fail, ok := s.resolveTranscriptReq(ctx, req, params.Session)
	if !ok {
		return fail
	}
	if params.ID == "" {
		return errResp(req.ID, CodeInvalidParams, "id is required")
	}
	if params.Since != nil && *params.Since < 0 {
		return errResp(req.ID, CodeInvalidParams, "since must not be negative")
	}
	reader, err := s.transcripts.Open(sess, params.Subagent)
	if err != nil {
		return transcriptOpenErr(req.ID, err)
	}
	it, found := reader.Item(params.ID)
	out := processOutputResult{Type: "process_output", State: transcript.ProcessUnknown}
	if found && it.Process != nil {
		out.State, out.ExitCode = it.Process.State, it.Process.ExitCode
	}
	path, hasPath := reader.ProcessPath(params.ID)
	if !hasPath {
		if !found || it.Process == nil {
			return errResp(req.ID, CodeNotFound, "no process "+params.ID)
		}
		text, _ := transcript.Truncate(it.Text, TranscriptItemMaxFieldBytes)
		out.fill([]byte(text), params.Since)
		return Response{ID: req.ID, Result: out}
	}
	resolved, exists, allowed := confineOutputPath(path, reader.SessionDirs())
	if !allowed {
		return errResp(req.ID, CodeForbidden, "process output is not in this session's directories")
	}
	if !exists {
		out.State = transcript.ProcessRunning
		return Response{ID: req.ID, Result: out}
	}
	if err := out.readFile(resolved, params.Since); err != nil {
		return errResp(req.ID, CodeUnavailable, "read process output: "+err.Error())
	}
	return Response{ID: req.ID, Result: out}
}

// window returns the part of a size-byte output to send: ProcessOutputMaxBytes
// from since, else the tail.
func (o *processOutputResult) window(size int64, since *int64) (start, n int64) {
	o.Size = size
	if since != nil {
		start = min(*since, size)
	} else {
		start = max(0, size-ProcessOutputMaxBytes)
	}
	n = min(size-start, ProcessOutputMaxBytes)
	o.Offset = start
	o.Truncated = start > 0 && since == nil || start+n < size
	return start, n
}

func (o *processOutputResult) fill(data []byte, since *int64) {
	start, n := o.window(int64(len(data)), since)
	o.Data = string(data[start : start+n])
}

func (o *processOutputResult) readFile(path string, since *int64) error {
	f, err := os.Open(path) //nolint:gosec // G304: confined to the session's directories by confineOutputPath
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	start, n := o.window(info.Size(), since)
	buf := make([]byte, n)
	got, err := f.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	o.Data = string(buf[:got])
	return nil
}

// confineOutputPath resolves a path a transcript named and accepts it only
// inside one of dirs, after symlinks on both sides. A path that does not exist
// yet is judged by its nearest existing ancestor. ~/.grok/auth.json is never
// served, whatever dirs say.
func confineOutputPath(path string, dirs []string) (resolved string, exists, ok bool) {
	if path == "" || !filepath.IsAbs(path) {
		return "", false, false
	}
	resolved, exists, err := resolveExisting(path)
	if err != nil {
		return "", false, false
	}
	if home, herr := os.UserHomeDir(); herr == nil {
		if auth, aerr := filepath.EvalSymlinks(filepath.Join(home, ".grok", "auth.json")); aerr == nil && auth == resolved {
			return "", false, false
		}
	}
	for _, d := range dirs {
		// A session directory may not exist yet; its ancestors still place it.
		root, _, rerr := resolveExisting(d)
		if rerr != nil {
			continue
		}
		if within(root, resolved) {
			return resolved, exists, true
		}
	}
	return "", false, false
}

// within reports whether path lies strictly below root.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveExisting resolves path with EvalSymlinks. When it does not exist, the
// nearest existing ancestor is resolved and the rest appended, refusing a rest
// that climbs with "..".
func resolveExisting(path string) (string, bool, error) {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r, true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}
	var rest []string
	cur := path
	for {
		parent, base := filepath.Dir(cur), filepath.Base(cur)
		if base == ".." || parent == cur {
			return "", false, errors.New("cannot resolve " + path)
		}
		rest = append([]string{base}, rest...)
		if r, err := filepath.EvalSymlinks(parent); err == nil {
			return filepath.Join(append([]string{r}, rest...)...), false, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", false, err
		}
		cur = parent
	}
}

// transcriptPins holds the readers session.transcript returned lately, by
// session and sub-agent.
type transcriptPins struct {
	mu   sync.Mutex
	pins map[trReaderKey]trPin
}

type trReaderKey struct{ session, subagent string }

type trPin struct {
	reader TranscriptReader
	at     time.Time
}

func (s *Server) pinTranscript(session, subagent string, r TranscriptReader) {
	p := &s.trPins
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pins == nil {
		p.pins = map[trReaderKey]trPin{}
	}
	p.pins[trReaderKey{session, subagent}] = trPin{reader: r, at: time.Now()}
}

// livePins returns the pins younger than transcriptPinTTL and drops the rest.
func (s *Server) livePins(now time.Time) map[trReaderKey]TranscriptReader {
	p := &s.trPins
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[trReaderKey]TranscriptReader{}
	for k, pin := range p.pins {
		if now.Sub(pin.at) >= transcriptPinTTL {
			delete(p.pins, k)
			continue
		}
		out[k] = pin.reader
	}
	return out
}

// transcriptWatch is one event subscriber's view of the transcript files: the
// size and mtime each file had when last announced, and when each reader last
// sent an event.
type transcriptWatch struct {
	seen map[trFileKey]trStamp
	sent map[trReaderKey]time.Time
}

type trFileKey struct {
	reader trReaderKey
	path   string
}

type trStamp struct {
	size  int64
	mtime time.Time
}

// transcriptPending is one event to send; marks are recorded as seen only
// once it is encoded.
type transcriptPending struct {
	ev    SessionEvent
	key   trReaderKey
	marks map[trFileKey]trStamp
}

func newTranscriptWatch() *transcriptWatch {
	return &transcriptWatch{seen: map[trFileKey]trStamp{}, sent: map[trReaderKey]time.Time{}}
}

func (w *transcriptWatch) commit(p transcriptPending, now time.Time) {
	for k, st := range p.marks {
		w.seen[k] = st
	}
	w.sent[p.key] = now
}

// collectTranscriptEvents stats the files of each pinned reader, and the
// AgentSessionPath of every other listed session, and returns a transcript
// event per reader whose files moved in size or mtime, at most one per
// TranscriptEventDebounce. A file seen for the first time is recorded quietly.
func (s *Server) collectTranscriptEvents(listed []domain.Session, w *transcriptWatch, now time.Time) []transcriptPending {
	watched := map[trReaderKey][]string{}
	for k, r := range s.livePins(now) {
		watched[k] = r.Files()
	}
	for _, sess := range listed {
		k := trReaderKey{session: sess.ID}
		if _, pinned := watched[k]; !pinned && sess.AgentSessionPath != "" {
			watched[k] = []string{sess.AgentSessionPath}
		}
	}
	visited := map[trFileKey]struct{}{}
	var out []transcriptPending
	for k, files := range watched {
		var marks map[trFileKey]trStamp
		var rev int64
		for _, path := range files {
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			fk := trFileKey{reader: k, path: path}
			visited[fk] = struct{}{}
			st := trStamp{size: info.Size(), mtime: info.ModTime()}
			prev, known := w.seen[fk]
			if !known {
				w.seen[fk] = st
				continue
			}
			if prev.size == st.size && prev.mtime.Equal(st.mtime) {
				continue
			}
			if marks == nil {
				marks = map[trFileKey]trStamp{}
			}
			marks[fk] = st
			rev = max(rev, st.size)
		}
		if marks == nil || now.Sub(w.sent[k]) < TranscriptEventDebounce {
			continue
		}
		out = append(out, transcriptPending{
			ev:    SessionEvent{Type: "transcript", ID: k.session, Subagent: k.subagent, Rev: max(rev, 1)},
			key:   k,
			marks: marks,
		})
	}
	for fk := range w.seen {
		if _, ok := visited[fk]; !ok {
			delete(w.seen, fk)
		}
	}
	for k := range w.sent {
		if _, ok := watched[k]; !ok {
			delete(w.sent, k)
		}
	}
	return out
}
