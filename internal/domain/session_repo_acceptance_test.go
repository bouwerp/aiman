package domain

import "testing"

// Acceptance tests for multi-repo sessions, piece P1. Children must not edit
// this file.

func TestSessionReposIsWorkspace(t *testing.T) {
	plain := Session{RepoName: "bouwerp/aiman", WorktreePath: "/r/aiman@x"}
	if plain.IsWorkspace() {
		t.Fatal("a session with no members is not a workspace session")
	}
	ws := Session{
		WorktreePath: "/r/host-tab@ws",
		Members: []SessionRepo{
			{Repo: "bouwerp/aiman", Branch: "host-tab", WorktreePath: "/r/host-tab@ws/aiman", Ord: 0},
			{Repo: "bouwerp/aiman-phone", Branch: "host-tab", BaseBranch: "main", WorktreePath: "/r/host-tab@ws/aiman-phone", Ord: 1},
		},
	}
	if !ws.IsWorkspace() {
		t.Fatal("a session with members is a workspace session")
	}
	if WorkspaceMarkerFile != "aiman-workspace" {
		t.Fatalf("WorkspaceMarkerFile = %q", WorkspaceMarkerFile)
	}
}

func TestSessionReposMemberFor(t *testing.T) {
	ws := Session{Members: []SessionRepo{
		{Repo: "bouwerp/aiman", WorktreePath: "/r/host-tab@ws/aiman"},
		{Repo: "bouwerp/aiman-phone", WorktreePath: "/r/host-tab@ws/aiman-phone"},
	}}
	cases := []struct {
		path, want string
		ok         bool
	}{
		{"/r/host-tab@ws/aiman", "bouwerp/aiman", true},
		{"/r/host-tab@ws/aiman/internal/x.go", "bouwerp/aiman", true},
		{"/r/host-tab@ws/aiman-phone/src", "bouwerp/aiman-phone", true},
		{"/r/host-tab@ws/aiman-phone/", "bouwerp/aiman-phone", true},
		{"/r/host-tab@ws", "", false},
		{"/r/host-tab@ws/aiman-other", "", false},
		{"/elsewhere", "", false},
	}
	for _, c := range cases {
		m, ok := ws.MemberFor(c.path)
		if ok != c.ok || m.Repo != c.want {
			t.Errorf("MemberFor(%q) = %q,%v want %q,%v", c.path, m.Repo, ok, c.want, c.ok)
		}
	}
}
