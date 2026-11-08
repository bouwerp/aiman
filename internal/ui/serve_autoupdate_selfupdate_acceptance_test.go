package ui

import (
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/remotesvc"
)

// A host installing a release on its own must not get a second install from
// the laptop while the first is still running.
func TestAutoUpdateWaitsForHostSelfUpdate(t *testing.T) {
	m := autoUpdateModel("v0.35.0")
	d := runningServe("aiman v0.34.0")
	d.SelfUpdate = &domain.SelfUpdateAttempt{Status: "started", To: "v0.35.0", At: time.Now().Add(-time.Minute)}
	if cmd := m.maybeAutoUpdateServe(d); cmd != nil {
		t.Fatal("the laptop updated a host whose own update is in progress")
	}
}

func TestAutoUpdateRunsWithoutLiveSelfUpdate(t *testing.T) {
	cases := []struct {
		name    string
		attempt *domain.SelfUpdateAttempt
	}{
		{"no attempt", nil},
		{"stale attempt", &domain.SelfUpdateAttempt{Status: "started", To: "v0.35.0", At: time.Now().Add(-remotesvc.OpTimeout - time.Minute)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := autoUpdateModel("v0.35.0")
			d := runningServe("aiman v0.34.0")
			d.SelfUpdate = tc.attempt
			if cmd := m.maybeAutoUpdateServe(d); cmd == nil {
				t.Fatal("a serve behind the client should be updated")
			}
		})
	}
}
