package limits

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	fileSourceMinAge  = time.Minute
	fileSourceTimeout = 5 * time.Second
	// fileTailBytes bounds what a file source reads of a file that keeps growing.
	fileTailBytes = 256 << 10

	// A session started yesterday can be the one still writing, so the search
	// covers more than the newest date folder.
	codexDayFolders = 3
	// A transcript just started holds no limits yet, so older ones are tried.
	codexMaxTranscripts = 8

	codexLimitID = "codex"
)

// NewCodexSource reads the limits Codex wrote into its newest transcript. The
// figures are only as fresh as the last Codex turn on this host.
func NewCodexSource(env Env) Source { return codexSource{home: env.Home} }

type codexSource struct{ home string }

func (codexSource) Name() string           { return "codex" }
func (codexSource) MinAge() time.Duration  { return fileSourceMinAge }
func (codexSource) Timeout() time.Duration { return fileSourceTimeout }

func (s codexSource) Read(ctx context.Context) Reading {
	root := filepath.Join(s.home, ".codex", "sessions")
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return Reading{Provider: "codex", State: StateUnavailable}
	}
	files := recentTranscripts(root)
	if len(files) == 0 {
		return codexUnknown("no Codex transcript yet")
	}
	for _, path := range files {
		if ctx.Err() != nil {
			return codexUnknown("timed out reading Codex transcripts")
		}
		tail, err := readTail(path, fileTailBytes)
		if err != nil {
			continue
		}
		if r, ok := lastCodexLimits(tail); ok {
			return r
		}
	}
	return codexUnknown("no rate limits in recent Codex transcripts")
}

func codexUnknown(reason string) Reading {
	return Reading{Provider: "codex", State: StateUnknown, Reason: reason, Source: "transcript"}
}

// recentTranscripts returns the newest transcripts, last written first, from
// the newest date folders of root/YYYY/MM/DD. Equal times fall back to the path,
// so the later date wins on a filesystem with coarse timestamps.
func recentTranscripts(root string) []string {
	type transcript struct {
		path    string
		modTime time.Time
	}
	var found []transcript
	for _, day := range newestFolders(root, 3, codexDayFolders) {
		entries, err := os.ReadDir(day)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			found = append(found, transcript{filepath.Join(day, e.Name()), info.ModTime()})
		}
	}
	slices.SortFunc(found, func(a, b transcript) int {
		return cmp.Or(b.modTime.Compare(a.modTime), strings.Compare(b.path, a.path))
	})
	paths := make([]string, 0, codexMaxTranscripts)
	for _, f := range found[:min(len(found), codexMaxTranscripts)] {
		paths = append(paths, f.path)
	}
	return paths
}

// newestFolders walks depth levels of date-named folders, newest name first,
// and returns at most limit folders at the bottom.
func newestFolders(dir string, depth, limit int) []string {
	if depth == 0 {
		return []string{dir}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range slices.Backward(entries) {
		if len(out) == limit {
			break
		}
		if e.IsDir() {
			out = append(out, newestFolders(filepath.Join(dir, e.Name()), depth-1, limit-len(out))...)
		}
	}
	return out
}

// readTail returns the last limit bytes of path, starting at a whole line when
// the file was cut.
func readTail(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open tail: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat tail: %w", err)
	}
	offset := max(info.Size()-limit, 0)
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek tail: %w", err)
	}
	tail, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return nil, fmt.Errorf("read tail: %w", err)
	}
	if offset == 0 {
		return tail, nil
	}
	if i := bytes.IndexByte(tail, '\n'); i >= 0 {
		return tail[i+1:], nil
	}
	return nil, nil
}

type codexLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type       string `json:"type"`
		RateLimits *struct {
			LimitID   string       `json:"limit_id"`
			PlanType  string       `json:"plan_type"`
			Primary   *codexWindow `json:"primary"`
			Secondary *codexWindow `json:"secondary"`
		} `json:"rate_limits"`
	} `json:"payload"`
}

type codexWindow struct {
	UsedPercent   *float64 `json:"used_percent"`
	WindowMinutes int      `json:"window_minutes"`
	ResetsAt      int64    `json:"resets_at"`
}

// lastCodexLimits finds the last usable limits line in tail. The line is parsed
// and never logged.
func lastCodexLimits(tail []byte) (Reading, bool) {
	for len(tail) > 0 {
		i := bytes.LastIndexByte(tail, '\n')
		line := tail[i+1:]
		tail = tail[:max(i, 0)]
		if !bytes.Contains(line, []byte(`"rate_limits"`)) {
			continue
		}
		if r, ok := parseCodexLine(line); ok {
			return r, true
		}
	}
	return Reading{}, false
}

// parseCodexLine reads a token_count line, including one with info null, which
// can still carry limits. Codex also writes lines for other limit ids such as
// "premium", whose windows are null, so those never replace the real figures.
func parseCodexLine(raw []byte) (Reading, bool) {
	var line codexLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return Reading{}, false
	}
	limits := line.Payload.RateLimits
	if line.Type != "event_msg" || line.Payload.Type != "token_count" || limits == nil {
		return Reading{}, false
	}
	if limits.LimitID != "" && limits.LimitID != codexLimitID {
		return Reading{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, line.Timestamp)
	if err != nil {
		return Reading{}, false
	}
	windows := make([]Window, 0, 2)
	for _, w := range []*codexWindow{limits.Primary, limits.Secondary} {
		if w != nil && w.UsedPercent != nil {
			windows = append(windows, w.window())
		}
	}
	if len(windows) == 0 {
		return Reading{}, false
	}
	return Reading{
		Provider:   "codex",
		Plan:       limits.PlanType,
		Windows:    windows,
		State:      StateOK,
		ObservedAt: at,
		Source:     "transcript",
	}, true
}

func (w *codexWindow) window() Window {
	out := Window{Label: codexWindowLabel(w.WindowMinutes), UsedPercent: *w.UsedPercent}
	if w.ResetsAt > 0 {
		out.ResetsAt = time.Unix(w.ResetsAt, 0)
	}
	return out
}

func codexWindowLabel(minutes int) string {
	const (
		hour = 60
		day  = 24 * hour
		week = 7 * day
	)
	switch {
	case minutes == week:
		return "week"
	case minutes <= 0:
		return "window"
	case minutes%day == 0:
		return fmt.Sprintf("%dd", minutes/day)
	case minutes%hour == 0:
		return fmt.Sprintf("%dh", minutes/hour)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
