package server

import (
	"context"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usecase"
)

type serverDoneItems struct{ s *Server }

// DoneItems returns the direct in-process adapter used when serve itself is
// creating a session.
func (s *Server) DoneItems() usecase.DoneItems { return serverDoneItems{s: s} }

func (d serverDoneItems) Ensure(_ context.Context, _ domain.RemoteExecutor, sess domain.Session) error {
	_, _, err := d.s.ensureDone(sess)
	return err
}
