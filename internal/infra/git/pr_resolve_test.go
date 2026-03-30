package git

import "testing"

func TestParseGitHubOwnerRepo(t *testing.T) {
	tests := []struct {
		raw   string
		owner string
		repo  string
		ok    bool
	}{
		{"git@github.com:acme/widget.git", "acme", "widget", true},
		{"https://github.com/acme/widget", "acme", "widget", true},
		{"https://github.com/acme/widget.git", "acme", "widget", true},
		{"git@git.example.com:myorg/myrepo", "myorg", "myrepo", true},
		{"", "", "", false},
		{"not-a-url", "", "", false},
	}
	for _, tt := range tests {
		o, r, ok := parseGitHubOwnerRepo(tt.raw)
		if ok != tt.ok || o != tt.owner || r != tt.repo {
			t.Errorf("parseGitHubOwnerRepo(%q) = (%q,%q,%v) want (%q,%q,%v)", tt.raw, o, r, ok, tt.owner, tt.repo, tt.ok)
		}
	}
}

func TestDeriveDisplayState(t *testing.T) {
	if deriveDisplayState("OPEN", true, false) != "draft" {
		t.Fatal("draft")
	}
	if deriveDisplayState("OPEN", false, true) != "merged" {
		t.Fatal("merged")
	}
	if deriveDisplayState("CLOSED", false, false) != "closed" {
		t.Fatal("closed")
	}
	if deriveDisplayState("OPEN", false, false) != "open" {
		t.Fatal("open")
	}
}
