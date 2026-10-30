package ui

import (
	"github.com/bouwerp/aiman/internal/server"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestCreatingPlaceholderMatchesLive(t *testing.T) {
	ph := domain.Session{
		ID: "pending-1", RemoteHost: "devbox",
		TerminalName: "feature/pb-1", Branch: "feature/pb-1", RepoName: "org/repo",
	}
	tests := []struct {
		name string
		live domain.Session
		want bool
	}{
		{
			name: "sanitized tmux name",
			live: domain.Session{ID: "real-1", RemoteHost: "devbox", TerminalName: "feature-pb-1"},
			want: true,
		},
		{
			name: "same branch and repo",
			live: domain.Session{ID: "real-1", RemoteHost: "devbox", Branch: "feature/pb-1", RepoName: "org/repo"},
			want: true,
		},
		{
			name: "different host",
			live: domain.Session{ID: "real-1", RemoteHost: "other", TerminalName: "feature-pb-1"},
			want: false,
		},
		{
			name: "unrelated branch",
			live: domain.Session{ID: "other", RemoteHost: "devbox", TerminalName: "other", Branch: "main", RepoName: "org/repo"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := creatingPlaceholderMatchesLive(ph, tt.live); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestShouldMergeDiscoveredSession(t *testing.T) {
	dbSessions := map[string]domain.Session{
		"known": {ID: "known"},
	}

	tests := []struct {
		name string
		s    domain.Session
		want bool
	}{
		{
			name: "active session without DB record still merges",
			s: domain.Session{
				ID:     "live",
				Status: domain.SessionStatusActive,
			},
			want: true,
		},
		{
			name: "inactive session with DB record still merges",
			s: domain.Session{
				ID:     "known",
				Status: domain.SessionStatusInactive,
			},
			want: true,
		},
		{
			name: "inactive session without DB record is skipped",
			s: domain.Session{
				ID:     "ghost",
				Status: domain.SessionStatusInactive,
			},
			want: false,
		},
		{
			name: "inactive session without ID is skipped",
			s: domain.Session{
				Status: domain.SessionStatusInactive,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldMergeDiscoveredSession(tt.s, dbSessions); got != tt.want {
				t.Fatalf("shouldMergeDiscoveredSession(%+v) = %v, want %v", tt.s, got, tt.want)
			}
		})
	}
}

func TestLookupPersistedSessionFallsBackToTerminalName(t *testing.T) {
	stored := domain.Session{
		ID: "db-id", Name: "impl", Group: "WTB-1925",
		RemoteHost: "regent0", TerminalName: "wtb-1925-fix",
	}
	byID := map[string]domain.Session{stored.ID: stored}
	byTerminal := map[string]domain.Session{sessionTerminalKey(stored): stored}

	liveSameID := domain.Session{ID: "db-id", RemoteHost: "regent0", TerminalName: "wtb-1925-fix"}
	got, ok := lookupPersistedSession(liveSameID, byID, byTerminal)
	if !ok || got.ID != "db-id" {
		t.Fatalf("id lookup: %+v ok=%v", got, ok)
	}

	liveNewID := domain.Session{ID: "fresh-uuid", RemoteHost: "regent0", TerminalName: "wtb-1925-fix"}
	got, ok = lookupPersistedSession(liveNewID, byID, byTerminal)
	if !ok || got.Group != "WTB-1925" || got.ID != "db-id" {
		t.Fatalf("tmux fallback: %+v ok=%v", got, ok)
	}

	miss := domain.Session{ID: "x", RemoteHost: "regent0", TerminalName: "other"}
	if _, ok := lookupPersistedSession(miss, byID, byTerminal); ok {
		t.Fatal("unrelated tmux must not match")
	}
}

func TestOverlayPersistedSessionFieldsKeepsNameAndGroup(t *testing.T) {
	live := domain.Session{
		ID: "s1", Status: domain.SessionStatusActive,
		TerminalName: "wtb-1", RemoteHost: "regent0",
	}
	stored := domain.Session{
		ID: "s1", Name: "impl", Group: "WTB-1925",
		IssueKey: "WTB-1925", Branch: "wtb-1925-fix",
		RepoName: "org/repo", WorktreePath: "/wt", LocalPath: "/local",
		AgentName: "claude", MutagenSyncID: "sync-1",
		AgentSessionID: "native-9", AgentSessionPath: "/tmp/t.jsonl",
		AgentTitle: "fix auth", HookState: "idle", HookStateSource: "idle_prompt",
	}
	got := overlayPersistedSessionFields(live, stored)
	if got.Name != "impl" || got.Group != "WTB-1925" {
		t.Fatalf("name=%q group=%q", got.Name, got.Group)
	}
	if got.TerminalName != "wtb-1" || got.RemoteHost != "regent0" {
		t.Fatalf("live identity clobbered: %+v", got)
	}
	if got.IssueKey != "WTB-1925" || got.RepoName != "org/repo" {
		t.Fatalf("other persisted fields dropped: %+v", got)
	}
	if got.AgentSessionID != "native-9" || got.AgentSessionPath != "/tmp/t.jsonl" {
		t.Fatalf("native session dropped: %+v", got)
	}
	if got.AgentTitle != "fix auth" || got.HookState != "idle" {
		t.Fatalf("hook fields dropped: %+v", got)
	}
}

func TestOverlayPersistedSessionFieldsKeepsTunnels(t *testing.T) {
	live := domain.Session{ID: "s1", RemoteHost: "regent0", TerminalName: "rev"}
	stored := domain.Session{ID: "s1", Tunnels: []domain.Tunnel{{LocalPort: 2345, RemotePort: 2345}}}
	got := overlayPersistedSessionFields(live, stored)
	if len(got.Tunnels) != 1 || got.Tunnels[0].LocalPort != 2345 {
		t.Fatalf("tunnels %+v", got.Tunnels)
	}
}

func TestOverlayPersistedSessionFieldsKeepsParentID(t *testing.T) {
	live := domain.Session{ID: "child", RemoteHost: "regent0", TerminalName: "rev"}
	stored := domain.Session{ID: "child", ParentID: "parent", TerminalName: "rev"}
	got := overlayPersistedSessionFields(live, stored)
	if got.ParentID != "parent" {
		t.Fatalf("live scan cannot see parent; overlay must keep stored, got %q", got.ParentID)
	}
}

func TestOverlayPersistedSessionFieldsKeepsBackend(t *testing.T) {
	live := domain.Session{ID: "s1", RemoteHost: "regent0", TerminalName: "feat"}
	stored := domain.Session{ID: "s1", Backend: domain.BackendPTY, TerminalName: "feat"}
	got := overlayPersistedSessionFields(live, stored)
	if got.Backend != domain.BackendPTY {
		t.Fatalf("live scan cannot see backend; overlay must keep stored %q, got %q", domain.BackendPTY, got.Backend)
	}
}

// After an agent switch the live row carries the new AgentName with an empty
// native id. Overlay must not reattach the previous agent's conversation.
func TestOverlayPersistedSessionFieldsDropsNativeIDAcrossAgentSwitch(t *testing.T) {
	live := domain.Session{
		ID: "s1", AgentName: "Grok Build CLI",
		RemoteHost: "regent0", TerminalName: "feat",
	}
	stored := domain.Session{
		ID: "s1", AgentName: "Claude Code",
		AgentSessionID: "00ca0f57", AgentSessionPath: "/home/code/.claude/projects/x.jsonl",
		AgentEnded: true, HookState: "idle", HookStateSource: "session_end", HookStateSeq: 3,
	}
	got := overlayPersistedSessionFields(live, stored)
	if got.AgentSessionID != "" || got.AgentSessionPath != "" {
		t.Fatalf("previous agent's native id must not survive a switch: %+v", got)
	}
	if got.AgentEnded || got.HookState != "" {
		t.Fatalf("previous agent's ended/hook state must not survive a switch: %+v", got)
	}
}

func TestOverlayKeepsStoredRole(t *testing.T) {
	live := domain.Session{ID: "rev", RemoteHost: "regent0", TerminalName: "rev"}
	stored := domain.Session{ID: "rev", Role: domain.SessionRoleReviewer, TerminalName: "rev"}
	if got := overlayPersistedSessionFields(live, stored); got.Role != domain.SessionRoleReviewer {
		t.Fatalf("live scan cannot see role; overlay must keep stored, got %q", got.Role)
	}
}

func TestSessionFromInfoMapsRole(t *testing.T) {
	if got := sessionFromInfo("regent0", server.SessionInfo{ID: "rev", Role: "reviewer"}); got.Role != domain.SessionRoleReviewer {
		t.Fatalf("role=%q", got.Role)
	}
}

func TestOverlayPersistedSessionFieldsKeepsReviewPR(t *testing.T) {
	live := domain.Session{ID: "rv", RemoteHost: "regent0", TerminalName: "pr-7"}
	stored := domain.Session{ID: "rv", ReviewPR: "o/r#7", TerminalName: "pr-7"}
	got := overlayPersistedSessionFields(live, stored)
	if got.ReviewPR != "o/r#7" {
		t.Fatalf("ReviewPR = %q, want o/r#7", got.ReviewPR)
	}
}
