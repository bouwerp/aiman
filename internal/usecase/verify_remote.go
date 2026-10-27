package usecase

import (
	"context"
	"encoding/json"
	"fmt"
)

// StartVerifyOnRemote asks serve to start a review for one session and
// returns the review status it reported.
func StartVerifyOnRemote(ctx context.Context, remote TerminalExecutor, sessionID string) (string, error) {
	raw, err := ServeRPC(ctx, remote, "verify.start", map[string]any{"session": sessionID})
	if err != nil {
		return "", err
	}
	var body struct {
		Review struct {
			Status string `json:"status"`
		} `json:"review"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", fmt.Errorf("decode verify review: %w", err)
	}
	return body.Review.Status, nil
}
