package domain

import "testing"

func TestFixerRoleIsVisibleAndCarriesFixPR(t *testing.T) {
	if SessionRoleFixer != "fixer" || SessionRoleFixer.Hidden() {
		t.Fatalf("role %q must be visible", SessionRoleFixer)
	}
	s := Session{Role: SessionRoleFixer, FixPR: "o/r#7"}
	cfg := SessionConfig{FixPR: "o/r#7"}
	if s.FixPR != cfg.FixPR || s.ReviewPR != "" {
		t.Fatalf("session %+v, config %+v", s, cfg)
	}
}
