package usecase

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestIsSessionMatch_LabelMatch(t *testing.T) {
	d := &SessionDiscoverer{}
	session := domain.Session{ID: "abc-123"}
	ms := domain.SyncSession{
		Labels: map[string]string{"aiman-id": "abc-123"},
	}
	if !d.isSessionMatch(session, ms) {
		t.Error("expected label-based match")
	}
}

func TestIsSessionMatch_LabelMismatch(t *testing.T) {
	d := &SessionDiscoverer{}
	session := domain.Session{ID: "abc-123"}
	ms := domain.SyncSession{
		Labels: map[string]string{"aiman-id": "other-id"},
	}
	if d.isSessionMatch(session, ms) {
		t.Error("expected no match for different label ID")
	}
}

func TestIsSessionMatch_StableNameMatch(t *testing.T) {
	d := &SessionDiscoverer{}
	session := domain.Session{ID: "abc-123"}
	ms := domain.SyncSession{Name: "aiman-sync-abc-123"}
	if !d.isSessionMatch(session, ms) {
		t.Error("expected stable name match")
	}
}

func TestIsSessionMatch_TmuxNameMatch(t *testing.T) {
	d := &SessionDiscoverer{}
	session := domain.Session{TmuxSession: "feature-branch"}
	ms := domain.SyncSession{Name: "feature-branch"}
	if !d.isSessionMatch(session, ms) {
		t.Error("expected tmux name match")
	}
}

func TestIsSessionMatch_ExactWorktreePath(t *testing.T) {
	d := &SessionDiscoverer{}
	session := domain.Session{
		WorktreePath: "/home/dev/repos/feature-branch",
	}
	ms := domain.SyncSession{
		RemotePath: "/home/dev/repos/feature-branch",
	}
	if !d.isSessionMatch(session, ms) {
		t.Error("expected exact worktree path match")
	}
}

func TestIsSessionMatch_ExactWorkingDirectory(t *testing.T) {
	d := &SessionDiscoverer{}
	session := domain.Session{
		WorktreePath:     "/home/dev/repos/feature-branch",
		WorkingDirectory: "/home/dev/repos/feature-branch/backend",
	}
	ms := domain.SyncSession{
		RemotePath: "/home/dev/repos/feature-branch/backend",
	}
	if !d.isSessionMatch(session, ms) {
		t.Error("expected exact WorkingDirectory path match")
	}
}

func TestIsSessionMatch_MutagenSubdirOfWorktree(t *testing.T) {
	d := &SessionDiscoverer{}
	session := domain.Session{
		WorktreePath: "/home/dev/repos/feature-branch",
	}
	ms := domain.SyncSession{
		RemotePath: "/home/dev/repos/feature-branch/backend/src",
	}
	if !d.isSessionMatch(session, ms) {
		t.Error("expected match when mutagen syncs a subdirectory of the worktree")
	}
}

func TestIsSessionMatch_WorkingDirIsSubdirOfMutagen(t *testing.T) {
	d := &SessionDiscoverer{}
	session := domain.Session{
		WorktreePath:     "/home/dev/repos/feature-branch",
		WorkingDirectory: "/home/dev/repos/feature-branch/backend/src",
	}
	ms := domain.SyncSession{
		RemotePath: "/home/dev/repos/feature-branch/backend",
	}
	if !d.isSessionMatch(session, ms) {
		t.Error("expected match when WorkingDirectory is within the mutagen sync path")
	}
}

func TestIsSessionMatch_NoFalsePositiveOnPartialName(t *testing.T) {
	d := &SessionDiscoverer{}
	session := domain.Session{
		WorktreePath: "/home/dev/repos/bar",
	}
	ms := domain.SyncSession{
		RemotePath: "/home/dev/repos/foobar",
	}
	if d.isSessionMatch(session, ms) {
		t.Error("should not match: /foobar is not a subdirectory of /bar")
	}
}

func TestIsSessionMatch_NoMatchUnrelatedPaths(t *testing.T) {
	d := &SessionDiscoverer{}
	session := domain.Session{
		WorktreePath:     "/home/dev/repos/feature-a",
		WorkingDirectory: "/home/dev/repos/feature-a/backend",
	}
	ms := domain.SyncSession{
		RemotePath: "/home/dev/repos/feature-b/backend",
	}
	if d.isSessionMatch(session, ms) {
		t.Error("should not match unrelated paths")
	}
}

func TestIsSessionMatch_EmptyRemotePath(t *testing.T) {
	d := &SessionDiscoverer{}
	session := domain.Session{
		WorktreePath: "/home/dev/repos/feature",
	}
	ms := domain.SyncSession{RemotePath: ""}
	if d.isSessionMatch(session, ms) {
		t.Error("should not match when remote path is empty")
	}
}

func TestIsSessionMatch_TrailingSlashNormalized(t *testing.T) {
	d := &SessionDiscoverer{}
	session := domain.Session{
		WorktreePath: "/home/dev/repos/feature/",
	}
	ms := domain.SyncSession{
		RemotePath: "/home/dev/repos/feature",
	}
	if !d.isSessionMatch(session, ms) {
		t.Error("expected match after trailing slash normalization")
	}
}

func TestExtractRepoNameFromURL_SSHFormat(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{"git@github.com:org/my-repo.git", "org/my-repo"},
		{"git@github.com:org/my-repo", "org/my-repo"},
		{"git@gitlab.com:company/project.git", "company/project"},
	}
	for _, tt := range tests {
		result := extractRepoNameFromURL(tt.url)
		if result != tt.expected {
			t.Errorf("extractRepoNameFromURL(%q) = %q, want %q", tt.url, result, tt.expected)
		}
	}
}

func TestExtractRepoNameFromURL_HTTPSFormat(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{"https://github.com/org/my-repo.git", "org/my-repo"},
		{"https://github.com/org/my-repo", "org/my-repo"},
		{"https://gitlab.com/company/project.git", "company/project"},
	}
	for _, tt := range tests {
		result := extractRepoNameFromURL(tt.url)
		if result != tt.expected {
			t.Errorf("extractRepoNameFromURL(%q) = %q, want %q", tt.url, result, tt.expected)
		}
	}
}

func TestExtractRepoNameFromURL_EmptyAndInvalid(t *testing.T) {
	if result := extractRepoNameFromURL(""); result != "" {
		t.Errorf("expected empty for empty URL, got %q", result)
	}
	if result := extractRepoNameFromURL("   "); result != "" {
		t.Errorf("expected empty for whitespace URL, got %q", result)
	}
}

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"/home/dev/repos/", "/home/dev/repos"},
		{"/home/dev/repos", "/home/dev/repos"},
		{"  /home/dev/repos  ", "/home/dev/repos"},
		{"C:\\Users\\dev\\repos", "C:/Users/dev/repos"},
		{"/", "/"},
	}
	for _, tt := range tests {
		result := normalizePath(tt.input)
		if result != tt.expected {
			t.Errorf("normalizePath(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}
