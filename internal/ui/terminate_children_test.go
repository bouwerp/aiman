package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

func childModel(sessions ...domain.Session) *Model {
	m := NewModel(&config.Config{Remotes: []config.Remote{{Host: "devbox", Root: "/home/code/repos"}}},
		nil, sessions, &mockSessionRepo{}, nil, nil, nil)
	m.allSessions = sessions
	return m
}

// Children are collected deepest first: a child torn down before its parent
// cannot be re-adopted by a parent that is already gone.
func TestTerminationChildrenAreDeepestFirst(t *testing.T) {
	m := childModel(
		domain.Session{ID: "root", Name: "root"},
		domain.Session{ID: "kid", Name: "kid", ParentID: "root"},
		domain.Session{ID: "grandkid", Name: "grandkid", ParentID: "kid"},
		domain.Session{ID: "unrelated", Name: "unrelated"},
	)
	got := m.terminationChildren(domain.Session{ID: "root"})
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "grandkid,kid" {
		t.Errorf("got %v, want [grandkid kid]", names)
	}
}

// Registering a session twice would run every teardown step against it twice.
func TestTerminationChildrenSkipsOnesAlreadyTerminating(t *testing.T) {
	m := childModel(
		domain.Session{ID: "root"},
		domain.Session{ID: "kid-a", Name: "kid-a", ParentID: "root"},
		domain.Session{ID: "kid-b", Name: "kid-b", ParentID: "root"},
	)
	m.terminatingSessions["kid-a"] = &terminatingSession{session: domain.Session{ID: "kid-a"}}

	got := m.terminationChildren(domain.Session{ID: "root"})
	if len(got) != 1 || got[0].ID != "kid-b" {
		t.Errorf("got %+v, want only kid-b", got)
	}
}

// Parent ids come off the wire and may point anywhere.
func TestTerminationChildrenSurvivesACycle(t *testing.T) {
	m := childModel(
		domain.Session{ID: "a", Name: "a", ParentID: "b"},
		domain.Session{ID: "b", Name: "b", ParentID: "a"},
	)
	if got := m.terminationChildren(domain.Session{ID: "a"}); len(got) != 1 {
		t.Errorf("got %+v, want one session", got)
	}
}

// A child created by an in-pane agent usually shares its parent's worktree, so
// one teardown must not delete the tree a live sibling is working in.
func TestTerminateSkipsAWorktreeASiblingStillUses(t *testing.T) {
	shared := "/home/code/repos/treasury-admin-dapp@fix-yield"
	m := childModel(
		domain.Session{ID: "parent", Name: "parent", RemoteHost: "devbox", WorktreePath: shared, RepoName: "org/treasury-admin-dapp"},
		domain.Session{ID: "child", Name: "child", RemoteHost: "devbox", WorktreePath: shared, RepoName: "org/treasury-admin-dapp", ParentID: "parent"},
	)
	err := m.runTerminateStep(stepIndex(t, false, "Removing git worktree"),
		domain.Session{ID: "child", RemoteHost: "devbox", WorktreePath: shared, RepoName: "org/treasury-admin-dapp"}, false)

	var skip skipReason
	if err == nil || !errors.As(err, &skip) {
		t.Fatalf("expected a skipReason, got %v", err)
	}
	if !strings.Contains(string(skip), "still used by parent") {
		t.Errorf("skip should name the surviving session, got %q", skip)
	}
}

// Once the sibling is on its way down too, nothing is left to protect.
func TestTerminateRemovesAWorktreeWhenEverySharerIsGoing(t *testing.T) {
	shared := "/home/code/repos/treasury-admin-dapp@fix-yield"
	m := childModel(
		domain.Session{ID: "parent", Name: "parent", RemoteHost: "devbox", WorktreePath: shared, RepoName: "org/treasury-admin-dapp"},
		domain.Session{ID: "child", Name: "child", RemoteHost: "devbox", WorktreePath: shared, RepoName: "org/treasury-admin-dapp", ParentID: "parent"},
	)
	m.terminatingSessions["parent"] = &terminatingSession{session: domain.Session{ID: "parent"}}

	if _, shared := m.worktreeStillInUse(domain.Session{ID: "child", WorktreePath: shared}); shared {
		t.Error("a sharer that is itself terminating must not block removal")
	}
}

func TestPluralSessions(t *testing.T) {
	if got := pluralSessions(1); got != "1 child session" {
		t.Errorf("got %q", got)
	}
	if got := pluralSessions(3); got != "3 child sessions" {
		t.Errorf("got %q", got)
	}
}
