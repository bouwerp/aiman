package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/remotesvc"
)

// The version already names a release in each case, so none of these reach GitHub.
func TestServiceOpScriptPinsTheRelease(t *testing.T) {
	const tag = "v0.29.3"
	cases := []struct {
		name string
		op   string
		auto bool
	}{
		{"install uses the client's release", "install", false},
		{"auto-update uses its target", "update", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			script, err := serviceOpScript(context.Background(), remotesvc.KindServe, c.op, tag, c.auto)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(script, remotesvc.KindServe.InstallPipeAt(tag)) {
				t.Fatalf("script does not install %s:\n%s", tag, script)
			}
		})
	}
	if _, err := serviceOpScript(context.Background(), remotesvc.KindServe, "nope", tag, false); err == nil {
		t.Fatal("unknown op accepted")
	}
}

// An auto-update with a non-release target must fail on the host, never fetch.
func TestServiceOpScriptRefusesABadAutoUpdateTarget(t *testing.T) {
	script, err := serviceOpScript(context.Background(), remotesvc.KindServe, "update", "dev", true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, "curl") {
		t.Fatalf("script fetches for an invalid tag:\n%s", script)
	}
}
