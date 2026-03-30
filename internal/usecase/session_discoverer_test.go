package usecase

import (
	"context"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

type recordingSyncEngine struct {
	terminated []string
}

func (r *recordingSyncEngine) StartSync(context.Context, string, string, string, map[string]string) error {
	return nil
}
func (r *recordingSyncEngine) StopSync(context.Context) error            { return nil }
func (r *recordingSyncEngine) GetStatus(context.Context) (string, error) { return "", nil }
func (r *recordingSyncEngine) ListSyncSessions(context.Context) ([]domain.SyncSession, error) {
	return nil, nil
}
func (r *recordingSyncEngine) TerminateSync(_ context.Context, name string) {
	r.terminated = append(r.terminated, name)
}

func TestHandleOrphanAimanNamedSync_TerminatesWhenNoTmuxPathMatch(t *testing.T) {
	rec := &recordingSyncEngine{}
	d := &SessionDiscoverer{syncEngine: rec}
	ms := domain.SyncSession{
		Name:       aimanSyncNamePrefix + "abc",
		RemotePath: "/home/code/repos/feature/backend",
	}
	if !d.handleOrphanAimanNamedSync(context.Background(), "", ms, nil) {
		t.Fatal("expected handler to take this mutagen session")
	}
	if len(rec.terminated) != 1 || rec.terminated[0] != ms.Name {
		t.Fatalf("expected terminate %q, got %v", ms.Name, rec.terminated)
	}
}

func TestHandleOrphanAimanNamedSync_NoTerminateWhenTmuxMatchesPath(t *testing.T) {
	rec := &recordingSyncEngine{}
	d := &SessionDiscoverer{syncEngine: rec}
	ms := domain.SyncSession{
		Name:       aimanSyncNamePrefix + "abc",
		RemotePath: "/home/code/repos/feature/backend",
	}
	tmux := []domain.Session{{
		WorktreePath:     "/home/code/repos/feature",
		WorkingDirectory: "/home/code/repos/feature/backend",
	}}
	if !d.handleOrphanAimanNamedSync(context.Background(), "", ms, tmux) {
		t.Fatal("expected handler to take this mutagen session")
	}
	if len(rec.terminated) != 0 {
		t.Fatalf("should not terminate when tmux paths match sync, got %v", rec.terminated)
	}
}

func TestHandleOrphanAimanNamedSync_NoTerminateWhenRemotePathEmpty(t *testing.T) {
	rec := &recordingSyncEngine{}
	d := &SessionDiscoverer{syncEngine: rec}
	ms := domain.SyncSession{Name: aimanSyncNamePrefix + "abc", RemotePath: ""}
	if !d.handleOrphanAimanNamedSync(context.Background(), "", ms, nil) {
		t.Fatal("expected handler to take this mutagen session")
	}
	if len(rec.terminated) != 0 {
		t.Fatalf("should not terminate without remote path, got %v", rec.terminated)
	}
}

func TestHandleOrphanAimanNamedSync_WrongDiscoverHostSkipsTerminate(t *testing.T) {
	rec := &recordingSyncEngine{}
	d := &SessionDiscoverer{syncEngine: rec}
	ms := domain.SyncSession{
		Name:           aimanSyncNamePrefix + "orphan",
		RemoteEndpoint: "code@otherbox",
		RemotePath:     "/home/x",
	}
	if !d.handleOrphanAimanNamedSync(context.Background(), "regent0", ms, nil) {
		t.Fatal("expected handler to consume aiman-sync entry")
	}
	if len(rec.terminated) != 0 {
		t.Fatalf("must not terminate another host's sync, got %v", rec.terminated)
	}
}

func TestHandleOrphanAimanNamedSync_NotOurPrefix(t *testing.T) {
	rec := &recordingSyncEngine{}
	d := &SessionDiscoverer{syncEngine: rec}
	ms := domain.SyncSession{Name: "other-sync", RemotePath: "/x"}
	if d.handleOrphanAimanNamedSync(context.Background(), "", ms, nil) {
		t.Fatal("expected false for non-aiman sync names")
	}
}

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
		{"/home//foo/../bar", "/home/bar"},
	}
	for _, tt := range tests {
		result := normalizePath(tt.input)
		if result != tt.expected {
			t.Errorf("normalizePath(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestDedupeDiscoveredSessions_DropsAimanSyncGhostsAndOrphanDup(t *testing.T) {
	host := "regent0"
	wt := "/home/dev/wt-branch"
	sessions := []domain.Session{
		{RemoteHost: host, TmuxSession: "wt-branch", Status: domain.SessionStatusActive, WorktreePath: wt, WorkingDirectory: wt},
		{RemoteHost: host, TmuxSession: "wt-branch", Status: domain.SessionStatusInactive, WorktreePath: wt, ID: "other-id"},
		{RemoteHost: host, TmuxSession: "aiman-sync-abc", WorktreePath: wt},
		{RemoteHost: host, TmuxSession: "wt-branch", Status: domain.SessionStatusInactive, WorktreePath: wt, WorkingDirectory: wt},
	}
	out := dedupeDiscoveredSessions(sessions)
	if len(out) != 1 {
		t.Fatalf("want 1 session, got %d: %+v", len(out), out)
	}
	if out[0].Status != domain.SessionStatusActive {
		t.Fatalf("expected active session kept")
	}
}
