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
// flag. Discovery never sees this id, so a same-agent restart always reads the
// file. freshStart skips resume entirely — used after an agent switch, when the
// previous agent's conversation must not be handed to the new binary.
func withRemoteNativeResume(ctx context.Context, remote remoteRunner, s *domain.Session, command string, freshStart bool) string {
	if s == nil {
		return command
	}
	if freshStart {
		s.AgentSessionID = ""
		s.AgentSessionPath = ""
		return command
	}
	nativeID := s.AgentSessionID
	nativePath := s.AgentSessionPath
	if id := agenthook.SafeSessionID(s.ID); id != "" && remote != nil {
		out, err := remote.Execute(ctx, fmt.Sprintf("cat \"$HOME/.aiman/native-sessions/%s\" 2>/dev/null || true", id))
		if err == nil {
			if n := agenthook.ParseStored([]byte(out)); n.ID != "" || n.State != "" || n.Ended {
				if n.ID != "" {
					nativeID = n.ID
				}
				if n.Path != "" {
					nativePath = n.Path
				}
				if agenthook.NativeIdentityFitsCommand(command, nativePath) {
					agenthook.ApplyReport(s, n, time.Now())
				} else {
					// Sidecar belongs to a different agent; drop it so the next
					// restart cannot keep forcing a foreign --resume.
					nativeID = ""
					nativePath = ""
					clearRemoteNativeSidecar(ctx, remote, s.ID)
				}
			}
		}
	}
	if !agenthook.NativeIdentityFitsCommand(command, nativePath) {
		return command
	}
	return agenthook.WithResume(command, nativeID)
}

// clearRemoteNativeSidecar drops the remote hook sidecar for sessionID so a
// subsequent agent start cannot resume the previous vendor conversation.
func clearRemoteNativeSidecar(ctx context.Context, remote remoteRunner, sessionID string) {
	id := agenthook.SafeSessionID(sessionID)
	if id == "" || remote == nil {
		return
	}
	_, _ = remote.Execute(ctx, fmt.Sprintf("rm -f \"$HOME/.aiman/native-sessions/%s\"", id))
}
