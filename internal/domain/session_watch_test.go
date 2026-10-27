package domain

import "testing"

func TestWatchesPRDefaults(t *testing.T) {
	cases := []struct {
		mode  SessionMode
		watch string
		want  bool
	}{
		{SessionModeAutonomous, "", true},
		{SessionModeInteractive, "", false},
		{SessionModeAutonomous, WatchPROff, false},
		{SessionModeInteractive, WatchPROn, true},
	}
	for _, c := range cases {
		s := Session{Mode: c.mode, WatchPR: c.watch}
		if got := s.WatchesPR(); got != c.want {
			t.Errorf("mode=%s watch=%q: got %v, want %v", c.mode, c.watch, got, c.want)
		}
	}
}
