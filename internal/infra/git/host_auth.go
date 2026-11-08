package git

import (
	"errors"

	"github.com/bouwerp/aiman/internal/infra/ssh"
)

// ErrHostAuthDenied marks a remote git call that GitHub refused because the
// host has no credentials of its own.
var ErrHostAuthDenied = errors.New("host cannot authenticate to GitHub")

// hostAuthMessage leads with the operator's fix. Bulk SSH calls never forward
// the laptop's agent, so the host needs a key or credential helper of its own.
const hostAuthMessage = "the host cannot authenticate to GitHub: give it its own credentials " +
	"(a deploy key, or gh auth login with the git credential helper); " +
	"aiman does not forward the laptop's ssh agent to bulk calls"

type hostAuthError struct{ err error }

func (e *hostAuthError) Error() string { return hostAuthMessage + ": " + e.err.Error() }

func (e *hostAuthError) Unwrap() []error { return []error{ErrHostAuthDenied, e.err} }

// WrapRemoteError returns err unchanged unless its text is a remote git auth
// denial (ssh.IsRemoteGitAuthDenial); then the result wraps ErrHostAuthDenied
// and err. An error already marked is returned as is, so nested calls do not
// repeat the message.
func WrapRemoteError(err error) error {
	if err == nil || errors.Is(err, ErrHostAuthDenied) || !ssh.IsRemoteGitAuthDenial(err.Error()) {
		return err
	}
	return &hostAuthError{err: err}
}
