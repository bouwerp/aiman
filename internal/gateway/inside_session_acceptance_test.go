package gateway

import (
	"errors"
	"strings"
	"testing"
)

func TestRefuseInsideSessionInsideAHolder(t *testing.T) {
	err := RefuseInsideSession(func() bool { return true })
	if !errors.Is(err, ErrInsideSession) {
		t.Fatalf("RefuseInsideSession(true) = %v, want ErrInsideSession", err)
	}
	if !strings.Contains(err.Error(), "inside a session's terminal") {
		t.Fatalf("message %q does not say it is inside a session's terminal", err.Error())
	}
}

func TestRefuseInsideSessionOutsideAHolder(t *testing.T) {
	if err := RefuseInsideSession(func() bool { return false }); err != nil {
		t.Fatalf("RefuseInsideSession(false) = %v, want nil", err)
	}
}

func TestErrInsideSessionMessage(t *testing.T) {
	const want = "aiman-gateway refuses to serve from inside a session's terminal: start it from the operator's shell or its systemd unit"
	if ErrInsideSession.Error() != want {
		t.Fatalf("ErrInsideSession = %q, want %q", ErrInsideSession.Error(), want)
	}
}
