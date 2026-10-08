package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/pane"
)

// PaneCache is the last screen the client applied for one session. History and
// View are split so a tmux poll can append lines that scrolled off without
// resending the viewport. Hash is the remote's hash, stored as sent.
type PaneCache struct {
	Hash        string
	Text        string
	History     string
	View        string
	HistorySize int
	OutputAt    time.Time
	TitleAt     time.Time
}

// PreviewUpdate is one preview poll. Unchanged means Text is the cached screen
// and the viewport should stay as it is. SinceOutput and SinceTitle are -1
// when the remote did not report that clock.
type PreviewUpdate struct {
	Text        string
	Unchanged   bool
	Cache       PaneCache
	SinceOutput time.Duration
	SinceTitle  time.Duration
}

// tmuxPaneProbe is one remote shell that returns a header and, only when the
// screen changed, the new lines. The first paint is capped at 200 lines.
// Snapshots still use CaptureTmuxPane, which reads the whole scrollback.
func tmuxPaneProbe(session string, haveHist int, haveHash string) string {
	return fmt.Sprintf(tmuxPaneProbeScript, session, haveHist, haveHash)
}

const tmuxPaneProbeScript = `s=%q
have_hist=%d
have_hash=%q
hist=$(tmux display-message -p -t "$s" '#{history_size}' 2>/dev/null || printf '0')
act=$(tmux display-message -p -t "$s" '#{session_activity}' 2>/dev/null || printf '0')
now=$(date +%%s)
rows=$(tmux display-message -p -t "$s" '#{pane_height}' 2>/dev/null || printf '24')
hashcmd() { if command -v sha256sum >/dev/null 2>&1; then sha256sum | awk '{print $1}'; else shasum -a 256 | awk '{print $1}'; fi; }
emit() { printf '@@AIMAN_PANE@@ %%s %%s %%s %%s %%s %%s\n' "$hist" "$1" "$act" "$now" "$rows" "$2"; }
view=$(tmux capture-pane -p -e -t "$s" 2>/dev/null || true)
vhash=$(printf '%%s' "$view" | hashcmd)
if [ "$hist" = "$have_hist" ] && [ "$vhash" = "$have_hash" ]; then
  emit "$vhash" UNCHANGED
  exit 0
fi
if [ "$have_hist" -ge 0 ] 2>/dev/null && [ "$hist" -gt "$have_hist" ] 2>/dev/null; then
  delta=$((hist - have_hist))
  if [ "$delta" -le 200 ]; then
    emit "$vhash" APPEND
    delta_text=$(tmux capture-pane -p -e -S "-$delta" -E -1 -t "$s" 2>/dev/null || true)
    printf '%%s\n@@VIEW@@\n%%s' "$delta_text" "$view"
    exit 0
  fi
fi
if [ "$hist" = "$have_hist" ]; then
  emit "$vhash" VIEW
  printf '%%s' "$view"
  exit 0
fi
emit "$vhash" REPLACE
tmux capture-pane -p -e -S -200 -t "$s"
`

type paneHeader struct {
	hist     int
	hash     string
	activity int64
	now      int64
	rows     int
	kind     string
	timesOK  bool
}

func parseTmuxPane(raw string, cache PaneCache) (PreviewUpdate, error) {
	raw = strings.TrimRight(raw, "\n")
	if raw == "" {
		return PreviewUpdate{}, fmt.Errorf("empty pane probe")
	}
	line, body, _ := strings.Cut(raw, "\n")
	hdr, err := parsePaneHeader(line)
	if err != nil {
		return PreviewUpdate{}, err
	}
	upd := PreviewUpdate{Cache: cache, SinceOutput: hdr.silence()}
	upd.Cache.Hash = hdr.hash
	upd.Cache.HistorySize = hdr.hist
	switch hdr.kind {
	case "UNCHANGED":
		upd.Unchanged = true
		upd.Text = cache.Text
		return upd, nil
	case "APPEND":
		return applyAppend(upd, body), nil
	case "VIEW":
		return applyView(upd, body), nil
	case "REPLACE":
		return applyReplace(upd, body, hdr.rows), nil
	default:
		return PreviewUpdate{}, fmt.Errorf("unknown pane probe kind %q", hdr.kind)
	}
}

func parsePaneHeader(line string) (paneHeader, error) {
	fields := strings.Fields(line)
	if len(fields) != 7 || fields[0] != "@@AIMAN_PANE@@" {
		return paneHeader{}, fmt.Errorf("bad pane header %q", line)
	}
	hist, err := strconv.Atoi(fields[1])
	if err != nil {
		return paneHeader{}, fmt.Errorf("bad history size %q", fields[1])
	}
	rows, err := strconv.Atoi(fields[5])
	if err != nil {
		return paneHeader{}, fmt.Errorf("bad row count %q", fields[5])
	}
	activity, aok := parseUnixSeconds(fields[3])
	now, nok := parseUnixSeconds(fields[4])
	return paneHeader{
		hist: hist, hash: fields[2], activity: activity, now: now, rows: rows,
		kind: fields[6], timesOK: aok && nok,
	}, nil
}

func (h paneHeader) silence() time.Duration {
	if !h.timesOK || h.now < h.activity {
		return -1
	}
	return time.Duration(h.now-h.activity) * time.Second
}

func applyAppend(upd PreviewUpdate, body string) PreviewUpdate {
	chunk, view := splitAppend(body)
	upd.Cache.History = joinPane(upd.Cache.History, chunk)
	upd.Cache.View = strings.TrimRight(view, "\n")
	upd.Text = joinPane(upd.Cache.History, upd.Cache.View)
	upd.Cache.Text = upd.Text
	return upd
}

func applyView(upd PreviewUpdate, body string) PreviewUpdate {
	upd.Cache.View = strings.TrimRight(body, "\n")
	upd.Text = joinPane(upd.Cache.History, upd.Cache.View)
	upd.Cache.Text = upd.Text
	return upd
}

func applyReplace(upd PreviewUpdate, body string, rows int) PreviewUpdate {
	history, view := splitViewport(body, rows)
	upd.Cache.History = history
	upd.Cache.View = view
	upd.Text = joinPane(history, view)
	upd.Cache.Text = upd.Text
	return upd
}

func splitAppend(body string) (string, string) {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	var hist []string
	for i, line := range lines {
		if line == "@@VIEW@@" {
			for len(hist) > 0 && hist[len(hist)-1] == "" {
				hist = hist[:len(hist)-1]
			}
			return strings.Join(hist, "\n"), strings.Join(lines[i+1:], "\n")
		}
		hist = append(hist, line)
	}
	return strings.Join(hist, "\n"), ""
}

func splitViewport(body string, rows int) (string, string) {
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return "", ""
	}
	lines := strings.Split(body, "\n")
	if rows <= 0 || rows >= len(lines) {
		return "", strings.Join(lines, "\n")
	}
	return strings.Join(lines[:len(lines)-rows], "\n"), strings.Join(lines[len(lines)-rows:], "\n")
}

func joinPane(history, view string) string {
	history = strings.TrimRight(history, "\n")
	view = strings.TrimRight(view, "\n")
	switch {
	case history == "":
		return view
	case view == "":
		return history
	default:
		return history + "\n" + view
	}
}

// RefreshPreview polls one session for a preview. An unchanged screen comes
// back without a body. A failure falls back to a full capture so the preview
// still paints when the probe is unavailable.
func RefreshPreview(ctx context.Context, remote PaneCapturer, s domain.Session, cache PaneCache) (PreviewUpdate, error) {
	if s.IsPTY() {
		return refreshPTYPreview(ctx, remote, s, cache)
	}
	return refreshTmuxPreview(ctx, remote, s, cache)
}

func refreshTmuxPreview(ctx context.Context, remote PaneCapturer, s domain.Session, cache PaneCache) (PreviewUpdate, error) {
	if s.TmuxSession == "" {
		return PreviewUpdate{}, fmt.Errorf("session has no tmux name")
	}
	haveHist := cache.HistorySize
	if cache.Hash == "" {
		haveHist = -1
	}
	out, err := remote.Execute(ctx, tmuxPaneProbe(s.TmuxSession, haveHist, cache.Hash))
	if err != nil {
		return fullPaneFallback(ctx, remote, s)
	}
	upd, perr := parseTmuxPane(out, cache)
	if perr != nil {
		return fullPaneFallback(ctx, remote, s)
	}
	if upd.SinceOutput >= 0 {
		upd.Cache.OutputAt = time.Now().Add(-upd.SinceOutput)
	}
	return upd, nil
}

func refreshPTYPreview(ctx context.Context, remote PaneCapturer, s domain.Session, cache PaneCache) (PreviewUpdate, error) {
	cmd := fmt.Sprintf("aiman pty capture %q --lines 0", terminalID(s))
	if cache.Hash != "" {
		cmd += fmt.Sprintf(" --have-hash %q", cache.Hash)
	}
	out, err := remote.Execute(ctx, remoteAimanPreamble+cmd)
	if err != nil {
		return fullPaneFallback(ctx, remote, s)
	}
	upd, perr := applyPTYPreview(out, cache, time.Now())
	if perr != nil {
		return fullPaneFallback(ctx, remote, s)
	}
	return upd, nil
}

func fullPaneFallback(ctx context.Context, remote PaneCapturer, s domain.Session) (PreviewUpdate, error) {
	text, err := CaptureSessionPane(ctx, remote, s)
	if err != nil {
		return PreviewUpdate{}, err
	}
	return PreviewUpdate{Text: text, Cache: PaneCache{Text: text, Hash: pane.ScreenHash(text)}}, nil
}

type ptyPreviewResult struct {
	Text         string          `json:"text"`
	Hash         string          `json:"hash"`
	Unchanged    bool            `json:"unchanged"`
	Rows         []pane.RowPatch `json:"rows"`
	LastOutput   string          `json:"last_output"`
	TitleChanged string          `json:"title_changed_at"`
}

func applyPTYPreview(raw string, cache PaneCache, now time.Time) (PreviewUpdate, error) {
	var res ptyPreviewResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &res); err != nil {
		text := strings.TrimRight(raw, "\n")
		return PreviewUpdate{Text: text, Cache: PaneCache{Text: text, Hash: pane.ScreenHash(text)}}, nil
	}
	upd := PreviewUpdate{Cache: cache, SinceOutput: -1, SinceTitle: -1}
	if stamp, ok := parsedStamp(res.LastOutput); ok {
		upd.Cache.OutputAt = stamp
		upd.SinceOutput = now.Sub(stamp)
	}
	if stamp, ok := parsedStamp(res.TitleChanged); ok {
		upd.Cache.TitleAt = stamp
		upd.SinceTitle = now.Sub(stamp)
	}
	if res.Hash != "" {
		upd.Cache.Hash = res.Hash
	}
	if res.Unchanged {
		upd.Unchanged = true
		upd.Text = cache.Text
		upd.Cache.Text = cache.Text
		return upd, nil
	}
	if len(res.Rows) > 0 {
		return applyPTYRows(upd, cache.Text, res.Rows)
	}
	if res.Text == "" {
		return PreviewUpdate{}, fmt.Errorf("pty capture returned no text")
	}
	upd.Text = res.Text
	upd.Cache.Text = res.Text
	if upd.Cache.Hash == "" {
		upd.Cache.Hash = pane.ScreenHash(res.Text)
	}
	return upd, nil
}

func applyPTYRows(upd PreviewUpdate, prev string, rows []pane.RowPatch) (PreviewUpdate, error) {
	text, ok := pane.ApplyScreen(prev, "", rows)
	if !ok {
		return PreviewUpdate{}, fmt.Errorf("pane row patch did not apply")
	}
	upd.Text = text
	upd.Cache.Text = text
	if upd.Cache.Hash == "" {
		upd.Cache.Hash = pane.ScreenHash(text)
	}
	return upd, nil
}

func parsedStamp(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
