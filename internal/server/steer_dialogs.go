package server

import (
	"github.com/bouwerp/aiman/internal/domain"
	infraAgent "github.com/bouwerp/aiman/internal/infra/agent"
)

// updateDialogShowing reports whether a pty session's screen ends in a
// startup update dialog (infraAgent.UpdateDialogs). Hooks can report idle
// while the dialog is up, and a note typed into it picks a menu row; the
// default row installs. Steering never dismisses it: the boot wait does that,
// and a dialog that survives it is left for the user.
func (s *Server) updateDialogShowing(sess domain.Session) bool {
	if sess.IsLegacyTmux() || s.pty == nil {
		return false
	}
	screen, err := s.pty.CaptureScreen(sess.ID)
	if err != nil {
		return false
	}
	_, ok := infraAgent.MatchUpdateDialog(screen)
	return ok
}
