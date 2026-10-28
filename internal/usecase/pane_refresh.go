package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/pane"
)

// PaneCache is the last screen the client applied for one session. Hash is
// the remote's hash, stored as sent.
type PaneCache struct {
	Hash     string
	Text     string
	OutputAt time.Time
	TitleAt  time.Time
	// HistorySize is never set by a PTY poll. The UI still copies it.
	//
	// Deprecated: removed in nl-13 T5.
	HistorySize int
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

// RefreshPreview polls one session's PTY for a preview. An unchanged screen
// comes back without a body. A failed delta poll falls back to a full capture
// so the preview still paints. A legacy tmux row returns its
// *domain.LegacyTmuxError.
func RefreshPreview(ctx context.Context, remote TerminalExecutor, s domain.Session, cache PaneCache) (PreviewUpdate, error) {
	if s.IsLegacyTmux() {
		return PreviewUpdate{}, domain.NewLegacyTmuxError(s)
	}
	return refreshPTYPreview(ctx, remote, s, cache)
}

// previewTailLines is the window a preview asks a PTY for. The session still
// keeps a much longer scrollback; that transcript is what makes a preview
// crawl when every change resends it.
const previewTailLines = 200

func refreshPTYPreview(ctx context.Context, remote TerminalExecutor, s domain.Session, cache PaneCache) (PreviewUpdate, error) {
	cmd := fmt.Sprintf("aiman pty capture %q --lines %d", terminalID(s), previewTailLines)
	if cache.Hash != "" {
		cmd += fmt.Sprintf(" --have-hash %q", cache.Hash)
	}
	out, err := remote.Execute(ctx, remoteAimanPreamble+cmd)
	if err != nil {
		return fullPTYPreview(ctx, remote, s)
	}
	upd, perr := applyPTYPreview(out, cache, time.Now())
	if perr != nil {
		return fullPTYPreview(ctx, remote, s)
	}
	return upd, nil
}

func fullPTYPreview(ctx context.Context, remote TerminalExecutor, s domain.Session) (PreviewUpdate, error) {
	text, err := CapturePTYPane(ctx, remote, terminalID(s), previewTailLines)
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
	Drop         int             `json:"drop"`
	Append       string          `json:"append"`
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
	if res.Append != "" || res.Drop > 0 {
		return applyPTYScroll(upd, cache.Text, res.Drop, res.Append)
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

func applyPTYScroll(upd PreviewUpdate, prev string, drop int, appendText string) (PreviewUpdate, error) {
	text, ok := pane.ApplyScroll(prev, drop, appendText)
	if !ok {
		return PreviewUpdate{}, fmt.Errorf("pane scroll did not apply")
	}
	upd.Text = text
	upd.Cache.Text = text
	if upd.Cache.Hash == "" {
		upd.Cache.Hash = pane.ScreenHash(text)
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
