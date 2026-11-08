//go:build acceptance

package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/server"
)

func TestReleaseHint(t *testing.T) {
	tests := []struct {
		name    string
		self    string
		remotes []string
		github  string
		want    ReleaseHint
		ok      bool
	}{
		{"remote newer", "v0.34.0", []string{"v0.35.0", "v0.33.0"}, "", ReleaseHint{Tag: "v0.35.0", Source: "remote"}, true},
		{"github newer", "v0.34.0", nil, "v0.36.0", ReleaseHint{Tag: "v0.36.0", Source: "github"}, true},
		{"github newest", "v0.34.0", []string{"v0.35.0"}, "v0.36.0", ReleaseHint{Tag: "v0.36.0", Source: "github"}, true},
		{"remote newest", "v0.34.0", []string{"v0.37.0", "v0.35.0"}, "v0.36.0", ReleaseHint{Tag: "v0.37.0", Source: "remote"}, true},
		{"nothing newer", "v0.34.0", []string{"v0.34.0", "v0.33.0"}, "v0.34.0", ReleaseHint{}, false},
		{"nothing at all", "v0.34.0", nil, "", ReleaseHint{}, false},
		{"dev self", "dev", []string{"v0.35.0"}, "v0.36.0", ReleaseHint{}, false},
		{"unparsable remotes ignored", "v0.34.0", []string{"dev", "", "garbage", "v0.35.0"}, "", ReleaseHint{Tag: "v0.35.0", Source: "remote"}, true},
		{"only unparsable", "v0.34.0", []string{"dev", "latest"}, "nonsense", ReleaseHint{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := releaseHint(tt.self, tt.remotes, tt.github)
			if ok != tt.ok {
				t.Fatalf("releaseHint(%q, %v, %q) ok = %v, want %v (hint %+v)", tt.self, tt.remotes, tt.github, ok, tt.ok, got)
			}
			if ok && got != tt.want {
				t.Fatalf("releaseHint(%q, %v, %q) = %+v, want %+v", tt.self, tt.remotes, tt.github, got, tt.want)
			}
		})
	}
}

func TestReleaseHintLine(t *testing.T) {
	h := ReleaseHint{Tag: "v0.35.0", Source: "remote"}
	if got, want := h.Line("v0.34.0"), "v0.34.0 · v0.35.0 available: aiman update"; got != want {
		t.Fatalf("Line = %q, want %q", got, want)
	}
}

// host_update does not need the user, but its toast is the laptop's notice
// that a host moved to a new release.
func TestNotifyToastHostUpdate(t *testing.T) {
	const text = "serve updated v0.34.0 → v0.35.0 on remote1"
	m := eventModel()
	cmd := m.notifyToast(server.SessionEvent{Type: "notify", Kind: "host_update", Text: text})
	if cmd == nil {
		t.Fatal("host_update raised no toast")
	}
	if !strings.Contains(m.snapshotToast, text) {
		t.Fatalf("host_update toast = %q, want it to contain %q", m.snapshotToast, text)
	}
	if m.snapshotToastError {
		t.Fatal("host_update is not an error toast")
	}
}

func TestNotifyToastAgentUpdateStaysQuiet(t *testing.T) {
	m := eventModel()
	if cmd := m.notifyToast(server.SessionEvent{Type: "notify", Kind: "agent_update"}); cmd != nil {
		t.Fatal("agent_update raised a toast")
	}
	if m.snapshotToast != "" {
		t.Fatalf("agent_update toast %q", m.snapshotToast)
	}
}
