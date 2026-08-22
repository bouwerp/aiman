package ui

import (
	"context"
	"fmt"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
)

type remoteRunner interface {
	Execute(ctx context.Context, cmd string) (string, error)
}

// withRemoteNativeResume fetches the vendor conversation id from the remote
// sidecar (laptop and remote DBs are different) and appends the agent's resume
// flag. Discovery never sees this id, so restart always reads the file.
func withRemoteNativeResume(ctx context.Context, remote remoteRunner, s *domain.Session, command string) string {
	if s == nil {
		return command
	}
	nativeID := s.AgentSessionID
	if id := agenthook.SafeSessionID(s.ID); id != "" && remote != nil {
		out, err := remote.Execute(ctx, fmt.Sprintf("cat \"$HOME/.aiman/native-sessions/%s\" 2>/dev/null || true", id))
		if err == nil {
			if n := agenthook.ParseStored([]byte(out)); n.ID != "" || n.State != "" || n.Ended {
				if n.ID != "" {
					nativeID = n.ID
				}
				agenthook.ApplyReport(s, n, time.Now())
			}
		}
	}
	return agenthook.WithResume(command, nativeID)
}
