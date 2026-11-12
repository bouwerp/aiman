package config

import "testing"

func TestSlackWritesOn(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{{"", true}, {"on", true}, {"true", true}, {"off", false}, {"OFF", false}, {"False", false}, {"  off\n", false}}
	for _, c := range cases {
		if got := (SlackConfig{Writes: c.v}).WritesOn(); got != c.want {
			t.Errorf("writes %q: %v, want %v", c.v, got, c.want)
		}
	}
}
