package usecase

import (
	"context"
	"errors"

	"github.com/bouwerp/aiman/internal/domain"
)

// DoneItems ensures a session's list has the evidence-backed Done-when item.
type DoneItems interface {
	Ensure(ctx context.Context, remote domain.RemoteExecutor, s domain.Session) error
}

// RemoteDoneItems is the laptop-side implementation: register the session with
// the remote serve, then ask it to create the item. An older serve without the
// method is treated as having no Done-when support.
type RemoteDoneItems struct{}

func (RemoteDoneItems) Ensure(ctx context.Context, remote domain.RemoteExecutor, s domain.Session) error {
	if err := RegisterSessionsOnRemote(ctx, remote, []domain.Session{s}); err != nil {
		return err
	}
	_, err := ServeRPC(ctx, remote, "todo.ensure_done", map[string]any{"session": s.ID})
	if errors.Is(err, ErrRPCUnsupported) || errors.Is(err, ErrServeNotRunning) {
		return nil
	}
	return err
}
