package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// HandoverPrompt tells the next agent to continue from the handover file.
const HandoverPrompt = `Read ` + domain.AimanHandoverFileName + ` — it is the handover from the previous agent: the to-do list, notes you have not seen, changed files and the last screen. Continue from it.`

// HandoverResult is the handover.write answer. Exists is false when serve
// could not leave .aiman_handover.md in the session directory.
type HandoverResult struct {
	Path    string `json:"path"`
	Written bool   `json:"written"`
	Exists  bool   `json:"exists"`
}

// HandoffMode is which file a restart should hand to the next agent.
type HandoffMode int

const (
	HandoffNone HandoffMode = iota
	HandoffHandover
	HandoffSummary
)

// WriteHandoverOnRemote asks serve to write the handover. It does not remove
// the session summary; PrepareRestartHandoff does that only after it accepts
// the file.
func WriteHandoverOnRemote(ctx context.Context, remote TerminalExecutor, sessionID, reason string) (HandoverResult, error) {
	raw, err := ServeRPC(ctx, remote, "handover.write", map[string]string{
		"session": sessionID,
		"reason":  reason,
	})
	if err != nil {
		return HandoverResult{}, err
	}
	var res HandoverResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return HandoverResult{}, fmt.Errorf("decode handover.write: %w", err)
	}
	return res, nil
}

// PrepareRestartHandoff prefers a serve handover. When that file is not
// available it asks the running agent for a session summary, bounded by
// summaryTimeout, and returns that attempt's note. Restart is not failed.
func PrepareRestartHandoff(ctx context.Context, remote domain.RemoteExecutor, s domain.Session, workingDir, reason string, summaryTimeout time.Duration) (HandoffMode, string) {
	res, err := WriteHandoverOnRemote(ctx, remote, s.ID, reason)
	if err == nil && res.Exists {
		return HandoffHandover, removeStaleSessionSummary(ctx, remote, workingDir)
	}

	summaryCtx, cancel := context.WithTimeout(ctx, summaryTimeout)
	defer cancel()
	created, note := CaptureSessionHandoffBestEffort(summaryCtx, remote, s, filepath.Join(workingDir, domain.AimanSessionSummaryFileName))
	if created {
		return HandoffSummary, note
	}
	return HandoffNone, note
}

// removeStaleSessionSummary drops the summary PrepareSession would otherwise
// hand to the next agent. The handover file is the handoff now.
func removeStaleSessionSummary(ctx context.Context, remote domain.RemoteExecutor, workingDir string) string {
	summaryPath := filepath.Join(workingDir, domain.AimanSessionSummaryFileName)
	if _, err := remote.Execute(ctx, fmt.Sprintf("rm -f %q %q", summaryPath, summaryPath+".tmp")); err != nil {
		return fmt.Sprintf("stale session summary not removed: %v", err)
	}
	return ""
}

// pointAtHandover queues a pointer at the handover file. A remote that cannot
// answer is ignored: the session is already running again.
func pointAtHandover(ctx context.Context, remote TerminalExecutor, sessionID string) {
	if _, err := ServeRPC(ctx, remote, "handover.point", map[string]string{
		"session": sessionID,
	}); err != nil {
		return
	}
}
