package ciwatch

import (
	"context"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestWatchedFilter(t *testing.T) {
	auto := testSession()
	interactive := auto
	interactive.Mode = domain.SessionModeInteractive
	forced := interactive
	forced.WatchPR = domain.WatchPROn
	ended := auto
	ended.AgentEnded = true
	inactive := auto
	inactive.Status = domain.SessionStatusInactive
	noDir := auto
	noDir.WorktreePath, noDir.WorkingDirectory = "", ""
	workingDirOnly := auto
	workingDirOnly.WorktreePath, workingDirOnly.WorkingDirectory = "", "/w/sub"

	cases := []struct {
		name string
		s    domain.Session
		want bool
	}{
		{"autonomous active with dir", auto, true},
		{"interactive default", interactive, false},
		{"interactive forced on", forced, true},
		{"agent ended", ended, false},
		{"inactive autonomous", inactive, false},
		{"no dir", noDir, false},
		{"working dir only", workingDirOnly, true},
	}
	for _, c := range cases {
		if got := Watched(c.s); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRunPollsWatchedAndKick(t *testing.T) {
	r := newRig(t)
	watched := testSession()
	unwatched := testSession()
	unwatched.ID, unwatched.WorktreePath, unwatched.Mode = "s2", "/w/s2", domain.SessionModeInteractive
	r.w.d.Sessions = fakeSessions{list: []domain.Session{watched, unwatched}}

	views := make(chan string, 8)
	r.gh.setPR(PRView{Number: 42, HeadSHA: "a", State: "OPEN"})
	r.gh.onView = func(dir string) { views <- dir }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.w.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	wait := func() string {
		t.Helper()
		select {
		case dir := <-views:
			return dir
		case <-time.After(2 * time.Second):
			t.Fatal("no PRView within 2s")
			return ""
		}
	}
	if dir := wait(); dir != "/w/s1" {
		t.Fatalf("first poll dir %q", dir)
	}
	r.w.Kick("s1")
	if dir := wait(); dir != "/w/s1" {
		t.Fatalf("kick poll dir %q", dir)
	}
	r.w.Kick("s2") // unwatched: no poll
	select {
	case dir := <-views:
		t.Fatalf("unwatched session polled: %q", dir)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSetWatchResetsState(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.store.RecordCheckFailure(ctx, "s1", "lint", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.MarkCheckEscalated(ctx, "s1", "lint", r.now); err != nil {
		t.Fatal(err)
	}
	if err := r.store.MarkCommentsSeen(ctx, "s1", []int64{1}); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Hour)
	if err := r.w.SetWatch(ctx, "s1", true); err != nil {
		t.Fatal(err)
	}
	st, err := r.w.Status(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Escalated) != 0 || !st.WatchSince.Equal(r.now) {
		t.Fatalf("state %+v", st)
	}
	if seen, _ := r.store.SeenComments(ctx, "s1"); len(seen) != 0 {
		t.Fatalf("seen %v", seen)
	}

	if err := r.w.SetWatch(ctx, "s1", false); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.w.Status(ctx, "s1"); !st.WatchSince.IsZero() {
		t.Fatalf("off kept WatchSince %v", st.WatchSince)
	}
}
