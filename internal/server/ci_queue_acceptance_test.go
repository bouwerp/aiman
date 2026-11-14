//go:build pileup_e

package server

// Contract (client-pileup-guard P3b, piece E), beyond the cigate symbols in
// internal/cigate/queue_acceptance_test.go:
//
//   - ci.local answers a run that waits for a slot with its record in status
//     "queued" (cigate.StatusQueued).
//   - ci.status gains `queue_position` (CIStatus.QueuePosition int,
//     json:"queue_position,omitempty"): the record's place in the host queue,
//     1 is next; absent or 0 once it runs.
//   - dropSession (session.forget and session.terminate) calls
//     Runner.Drop(id) before Runner.Forget(id): a queued run of the session
//     never starts, and the queue moves up behind it.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/cigate"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

type ciQueueStatus struct {
	Status struct {
		Record        *cigate.Record `json:"record"`
		QueuePosition int            `json:"queue_position"`
	} `json:"status"`
}

// ciQueueServer is a serve with sessions ids, each on its own checkout whose
// gate list appends the session id to marks/order and holds until
// marks/release-<id> exists. One run at a time.
func ciQueueServer(t *testing.T, ids ...string) (*Server, string) {
	t.Helper()
	marks := t.TempDir()
	var sessions []domain.Session
	for _, id := range ids {
		run := "echo " + id + " >> " + filepath.Join(marks, "order") +
			"; while [ ! -f " + filepath.Join(marks, "release-"+id) + " ]; do sleep 0.02; done"
		s := todoSession(id)
		s.WorktreePath = ciRepo(t, "commands:\n  - name: hold\n    run: \""+run+"\"\n")
		s.RepoName = "o/r"
		sessions = append(sessions, s)
	}
	srv, _, _ := todoServer(t, sessions...)
	srv.SetCILocal(CILocalDeps{
		Runner: &cigate.Runner{
			Dir:         filepath.Join(t.TempDir(), "ci-local"),
			MaxParallel: 1,
			TmpDir:      filepath.Join(shortTempDir(t), "tmp"),
		},
		Repos:    map[string]config.RepoBootstrap{},
		LookupPR: (&fakePRLookup{}).lookup,
	})
	t.Cleanup(func() {
		// Let any held run finish so its goroutine does not outlive the test.
		for _, id := range ids {
			_ = os.WriteFile(filepath.Join(marks, "release-"+id), nil, 0o600)
		}
	})
	return srv, marks
}

func ciStarted(marks string) []string {
	b, err := os.ReadFile(filepath.Join(marks, "order"))
	if err != nil {
		return nil
	}
	return strings.Fields(string(b))
}

func ciRelease(t *testing.T, marks, id string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(marks, "release-"+id), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func ciEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func ciQueueStatusOf(t *testing.T, srv *Server, id string) ciQueueStatus {
	t.Helper()
	var st ciQueueStatus
	rpcOK(t, srv, "", "ci.status", map[string]any{"session": id}, &st)
	return st
}

const (
	qA = "aaaaaaaa-q1"
	qB = "bbbbbbbb-q2"
	qC = "cccccccc-q3"
)

func TestAcceptanceCIStatusShowsAQueuedRunWithItsPosition(t *testing.T) {
	srv, marks := ciQueueServer(t, qA, qB, qC)
	rpcOK(t, srv, "", "ci.local", map[string]any{"session": qA}, nil)
	ciEventually(t, qA+" starts", func() bool { return len(ciStarted(marks)) >= 1 })

	for i, id := range []string{qB, qC} {
		var local struct {
			Record cigate.Record `json:"record"`
			Done   bool          `json:"done"`
		}
		rpcOK(t, srv, "", "ci.local", map[string]any{"session": id}, &local)
		if local.Done || local.Record.Status != cigate.StatusQueued {
			t.Fatalf("ci.local %s = %+v, want a queued record", id, local)
		}
		st := ciQueueStatusOf(t, srv, id)
		if st.Status.Record == nil || st.Status.Record.Status != cigate.StatusQueued || st.Status.QueuePosition != i+1 {
			t.Fatalf("ci.status %s = record %+v position %d, want queued at %d", id, st.Status.Record, st.Status.QueuePosition, i+1)
		}
	}
	st := ciQueueStatusOf(t, srv, qA)
	if st.Status.Record == nil || st.Status.Record.Status != cigate.StatusRunning || st.Status.QueuePosition != 0 {
		t.Fatalf("ci.status %s = record %+v position %d, want running and no position", qA, st.Status.Record, st.Status.QueuePosition)
	}

	ciRelease(t, marks, qA)
	ciEventually(t, qB+" starts next", func() bool { return len(ciStarted(marks)) >= 2 })
	if got := ciStarted(marks); got[1] != qB {
		t.Fatalf("started %v, want %s second", got, qB)
	}
	if st := ciQueueStatusOf(t, srv, qC); st.Status.QueuePosition != 1 {
		t.Fatalf("ci.status %s position %d, want 1", qC, st.Status.QueuePosition)
	}
	ciRelease(t, marks, qB)
	ciRelease(t, marks, qC)
	ciEventually(t, qC+" passes", func() bool {
		st := ciQueueStatusOf(t, srv, qC)
		return st.Status.Record != nil && st.Status.Record.Status == cigate.StatusPass
	})
}

func TestAcceptanceForgettingASessionDropsItsQueuedRun(t *testing.T) {
	srv, marks := ciQueueServer(t, qA, qB, qC)
	rpcOK(t, srv, "", "ci.local", map[string]any{"session": qA}, nil)
	ciEventually(t, qA+" starts", func() bool { return len(ciStarted(marks)) >= 1 })
	rpcOK(t, srv, "", "ci.local", map[string]any{"session": qB}, nil)
	rpcOK(t, srv, "", "ci.local", map[string]any{"session": qC}, nil)
	if st := ciQueueStatusOf(t, srv, qC); st.Status.QueuePosition != 2 {
		t.Fatalf("ci.status %s position %d, want 2", qC, st.Status.QueuePosition)
	}

	rpcOK(t, srv, "", "session.forget", map[string]any{"id": qB}, nil)
	if st := ciQueueStatusOf(t, srv, qC); st.Status.QueuePosition != 1 {
		t.Fatalf("ci.status %s position %d after %s was forgotten, want 1", qC, st.Status.QueuePosition, qB)
	}

	ciRelease(t, marks, qA)
	ciRelease(t, marks, qB)
	ciEventually(t, qC+" starts after the head", func() bool { return len(ciStarted(marks)) >= 2 })
	ciRelease(t, marks, qC)
	ciEventually(t, qC+" passes", func() bool {
		st := ciQueueStatusOf(t, srv, qC)
		return st.Status.Record != nil && st.Status.Record.Status == cigate.StatusPass
	})
	if got := ciStarted(marks); slices.Contains(got, qB) {
		t.Fatalf("started %v: the forgotten session's queued run started", got)
	}
}
