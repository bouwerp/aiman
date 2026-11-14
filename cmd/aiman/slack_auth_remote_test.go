package main

import (
	"testing"
	"time"
)

func TestSlackLoginTimeout(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
		arg  string
		bad  bool
	}{
		{"", 5 * time.Minute, "5m", false},
		{"3m", 3 * time.Minute, "3m", false},
		{"1m30s", 90 * time.Second, "1m30s", false},
		{"+2m", 2 * time.Minute, "2m0s", false},
		{"-1m", 0, "", true},
		{"soon", 0, "", true},
	}
	for _, c := range cases {
		d, arg, err := slackLoginTimeout(map[string]string{"timeout": c.raw})
		if (err != nil) != c.bad || d != c.want || arg != c.arg {
			t.Errorf("%q: got %v %q %v", c.raw, d, arg, err)
		}
	}
}
