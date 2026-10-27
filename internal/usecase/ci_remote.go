package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrCIUnsupported means the remote's serve predates ci.get and ci.watch.
// Errors carrying it also match ErrRPCUnsupported.
var ErrCIUnsupported = errors.New("remote aiman has no PR watch")

// CIStatus is a session's PR watch as serve reports it. Escalated names the
// checks that failed too many fixes in a row and now need the user.
type CIStatus struct {
	Watch     bool     `json:"watch"`
	PRNumber  int      `json:"pr_number"`
	PRState   string   `json:"pr_state"`
	Escalated []string `json:"escalated"`
	LastError string   `json:"last_error"`
}

// GetCIStatusOnRemote reads the PR watch for one session. serve answers from
// its own state; no GitHub call happens on this path.
func GetCIStatusOnRemote(ctx context.Context, remote TerminalExecutor, sessionID string) (CIStatus, error) {
	return callCI(ctx, remote, "ci.get", map[string]any{"session": sessionID})
}

// SetPRWatchOnRemote turns the PR watch for one session on or off and returns
// the status after the change.
func SetPRWatchOnRemote(ctx context.Context, remote TerminalExecutor, sessionID string, on bool) (CIStatus, error) {
	return callCI(ctx, remote, "ci.watch", map[string]any{"session": sessionID, "on": on})
}

func callCI(ctx context.Context, remote TerminalExecutor, method string, params map[string]any) (CIStatus, error) {
	raw, err := ServeRPC(ctx, remote, method, params)
	if errors.Is(err, ErrRPCUnsupported) {
		return CIStatus{}, fmt.Errorf("%w: %w", ErrCIUnsupported, err)
	}
	if err != nil {
		return CIStatus{}, err
	}
	return parseCIResult(string(raw))
}

// parseCIResult decodes the {"type":"ci","ci":{…}} answer.
func parseCIResult(out string) (CIStatus, error) {
	var body struct {
		CI *CIStatus `json:"ci"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		return CIStatus{}, fmt.Errorf("decode ci result: %w", err)
	}
	if body.CI == nil {
		return CIStatus{}, errors.New("decode ci result: no ci object")
	}
	return *body.CI, nil
}
