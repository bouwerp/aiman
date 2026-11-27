package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// Acceptance tests for multi-repo sessions, piece P6: the PR watcher's state
// is keyed by (session_id, repo). A plain session uses repo "", so the
// repo-less methods keep their meaning. Children must not edit this file.

func TestCIStateWorkspaceWatchPerRepo(t *testing.T) {
	ctx := context.Background()
	r := newCIRepo(t)
	since := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	a := domain.PRWatchState{SessionID: "ws", Repo: "o/a", PRNumber: 11, PRState: "OPEN", HeadSHA: "a1", WatchSince: since}
	b := domain.PRWatchState{SessionID: "ws", Repo: "o/b", PRNumber: 22, PRState: "OPEN", HeadSHA: "b1", LastError: "gh: boom"}
	for _, st := range []domain.PRWatchState{a, b} {
		if err := r.SavePRWatch(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	gotA, err := r.LoadPRWatchRepo(ctx, "ws", "o/a")
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := r.LoadPRWatchRepo(ctx, "ws", "o/b")
	if err != nil {
		t.Fatal(err)
	}
	if !sameWatch(gotA, a) {
		t.Fatalf("o/a = %+v, want %+v", gotA, a)
	}
	if !sameWatch(gotB, b) {
		t.Fatalf("o/b = %+v, want %+v (saving o/b must not overwrite o/a)", gotB, b)
	}
	// The repo-less read is the plain session's row (repo ""), which a
	// workspace session never writes.
	plain, err := r.LoadPRWatch(ctx, "ws")
	if err != nil {
		t.Fatal(err)
	}
	if plain.PRNumber != 0 || plain.Repo != "" {
		t.Fatalf("repo-less row = %+v, want the zero state", plain)
	}
	// A member never polled reads as the zero state with its keys set.
	none, err := r.LoadPRWatchRepo(ctx, "ws", "o/c")
	if err != nil || none.SessionID != "ws" || none.Repo != "o/c" || none.PRNumber != 0 {
		t.Fatalf("unknown member = %+v, %v", none, err)
	}
}

func sameWatch(got, want domain.PRWatchState) bool {
	return got.SessionID == want.SessionID && got.Repo == want.Repo && got.PRNumber == want.PRNumber &&
		got.PRState == want.PRState && got.HeadSHA == want.HeadSHA && got.LastError == want.LastError &&
		got.WatchSince.Equal(want.WatchSince) && got.LastPolledAt.Equal(want.LastPolledAt) && len(got.Escalated) == 0
}

func TestCIStateWorkspaceFailuresAndEscalationsPerRepo(t *testing.T) {
	ctx := context.Background()
	r := newCIRepo(t)
	for _, sha := range []string{"1", "2", "3"} {
		if _, err := r.RecordCheckFailureRepo(ctx, "ws", "o/a", "build", sha); err != nil {
			t.Fatal(err)
		}
	}
	// The same check name on another member counts on its own.
	if n, err := r.RecordCheckFailureRepo(ctx, "ws", "o/b", "build", "1"); err != nil || n != 1 {
		t.Fatalf("o/b failures = %d, %v; want 1", n, err)
	}
	if n, err := r.RecordCheckFailureRepo(ctx, "ws", "o/a", "build", "4"); err != nil || n != 4 {
		t.Fatalf("o/a failures = %d, %v; want 4", n, err)
	}

	at := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	if fresh, err := r.MarkCheckEscalatedRepo(ctx, "ws", "o/a", "build", at); err != nil || !fresh {
		t.Fatalf("first o/a escalation fresh=%v err=%v", fresh, err)
	}
	if fresh, err := r.MarkCheckEscalatedRepo(ctx, "ws", "o/a", "build", at); err != nil || fresh {
		t.Fatalf("second o/a escalation fresh=%v err=%v", fresh, err)
	}
	if fresh, err := r.MarkCheckEscalatedRepo(ctx, "ws", "o/b", "build", at); err != nil || !fresh {
		t.Fatalf("o/b escalation is its own: fresh=%v err=%v", fresh, err)
	}
	stA, _ := r.LoadPRWatchRepo(ctx, "ws", "o/a")
	if !reflect.DeepEqual(stA.Escalated, []string{"build"}) {
		t.Fatalf("o/a escalated = %v", stA.Escalated)
	}
	if plain, _ := r.LoadPRWatch(ctx, "ws"); len(plain.Escalated) != 0 {
		t.Fatalf("repo-less state shows member escalations: %v", plain.Escalated)
	}

	// Clearing o/a's check leaves o/b's failures and escalation alone.
	if err := r.ClearCheckRepo(ctx, "ws", "o/a", "build"); err != nil {
		t.Fatal(err)
	}
	if stA, _ := r.LoadPRWatchRepo(ctx, "ws", "o/a"); len(stA.Escalated) != 0 {
		t.Fatalf("o/a escalated after clear = %v", stA.Escalated)
	}
	if n, _ := r.RecordCheckFailureRepo(ctx, "ws", "o/a", "build", "1"); n != 1 {
		t.Fatalf("o/a failures after clear = %d, want 1", n)
	}
	if stB, _ := r.LoadPRWatchRepo(ctx, "ws", "o/b"); !reflect.DeepEqual(stB.Escalated, []string{"build"}) {
		t.Fatalf("o/b escalated after clearing o/a = %v", stB.Escalated)
	}
	if n, _ := r.RecordCheckFailureRepo(ctx, "ws", "o/b", "build", "1"); n != 1 {
		t.Fatalf("o/b failures after clearing o/a = %d, want 1 (sha 1 already counted)", n)
	}
}

func TestCIStateWorkspacePlainMethodsAreRepoEmpty(t *testing.T) {
	ctx := context.Background()
	r := newCIRepo(t)
	if _, err := r.RecordCheckFailure(ctx, "s1", "lint", "a"); err != nil {
		t.Fatal(err)
	}
	if n, err := r.RecordCheckFailureRepo(ctx, "s1", "", "lint", "b"); err != nil || n != 2 {
		t.Fatalf("repo \"\" shares the plain rows: n=%d err=%v", n, err)
	}
	at := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	if fresh, _ := r.MarkCheckEscalated(ctx, "s1", "lint", at); !fresh {
		t.Fatal("plain escalation not fresh")
	}
	if fresh, _ := r.MarkCheckEscalatedRepo(ctx, "s1", "", "lint", at); fresh {
		t.Fatal("repo \"\" escalation is the plain one and must not be fresh again")
	}
	if err := r.SavePRWatch(ctx, domain.PRWatchState{SessionID: "s1", PRNumber: 7}); err != nil {
		t.Fatal(err)
	}
	st, err := r.LoadPRWatchRepo(ctx, "s1", "")
	if err != nil || st.PRNumber != 7 || !reflect.DeepEqual(st.Escalated, []string{"lint"}) {
		t.Fatalf("LoadPRWatchRepo(\"\") = %+v, %v", st, err)
	}
}

func TestCIStateWorkspaceResetClearsEveryRepo(t *testing.T) {
	ctx := context.Background()
	r := newCIRepo(t)
	at := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	for _, repo := range []string{"", "o/a", "o/b"} {
		if err := r.SavePRWatch(ctx, domain.PRWatchState{SessionID: "ws", Repo: repo, PRNumber: 5}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.RecordCheckFailureRepo(ctx, "ws", repo, "build", "x"); err != nil {
			t.Fatal(err)
		}
		if _, err := r.MarkCheckEscalatedRepo(ctx, "ws", repo, "build", at); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.ResetPRWatch(ctx, "ws"); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{"", "o/a", "o/b"} {
		st, err := r.LoadPRWatchRepo(ctx, "ws", repo)
		if err != nil || st.PRNumber != 0 || len(st.Escalated) != 0 {
			t.Fatalf("%q after reset = %+v, %v", repo, st, err)
		}
		if n, _ := r.RecordCheckFailureRepo(ctx, "ws", repo, "build", "x"); n != 1 {
			t.Fatalf("%q failures survived reset: %d", repo, n)
		}
	}
}

// A database written before the repo key existed keeps its rows: they become
// the plain session's (repo "") state.
func TestCIStateWorkspaceMigratesOldTables(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "aiman.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE ci_watch(session_id TEXT PRIMARY KEY, pr_number INTEGER NOT NULL DEFAULT 0, pr_state TEXT NOT NULL DEFAULT '', head_sha TEXT NOT NULL DEFAULT '', watch_since DATETIME, last_polled_at DATETIME, last_error TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE ci_check_failures(session_id TEXT NOT NULL, check_name TEXT NOT NULL, head_sha TEXT NOT NULL, failed_at DATETIME NOT NULL, PRIMARY KEY(session_id, check_name, head_sha))`,
		`CREATE TABLE ci_escalations(session_id TEXT NOT NULL, check_name TEXT NOT NULL, escalated_at DATETIME NOT NULL, PRIMARY KEY(session_id, check_name))`,
		`CREATE TABLE ci_seen_comments(session_id TEXT NOT NULL, comment_id INTEGER NOT NULL, PRIMARY KEY(session_id, comment_id))`,
		`INSERT INTO ci_watch(session_id, pr_number, pr_state, head_sha, last_error) VALUES('s1', 9, 'OPEN', 'abc', '')`,
		`INSERT INTO ci_check_failures VALUES('s1', 'lint', 'abc', '2026-11-01 00:00:00')`,
		`INSERT INTO ci_escalations VALUES('s1', 'lint', '2026-11-01 00:00:00')`,
	} {
		if _, err := old.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := NewRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	st, err := r.LoadPRWatch(ctx, "s1")
	if err != nil || st.PRNumber != 9 || st.HeadSHA != "abc" || !reflect.DeepEqual(st.Escalated, []string{"lint"}) {
		t.Fatalf("migrated state = %+v, %v", st, err)
	}
	if n, _ := r.RecordCheckFailure(ctx, "s1", "lint", "abc"); n != 1 {
		t.Fatalf("migrated failures = %d, want 1", n)
	}
	// After migration a second repo can be stored for the same session.
	if err := r.SavePRWatch(ctx, domain.PRWatchState{SessionID: "s1", Repo: "o/a", PRNumber: 3}); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.LoadPRWatch(ctx, "s1"); st.PRNumber != 9 {
		t.Fatalf("plain row overwritten by a member save: %+v", st)
	}
}
