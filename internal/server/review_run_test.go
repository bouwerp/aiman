package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/review"
)

type fakeDocker struct {
	mu    sync.Mutex
	calls [][]string
	out   string
	exit  int
}

func (f *fakeDocker) run(_ context.Context, _ string, args ...string) (string, int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	f.mu.Unlock()
	return f.out, f.exit, nil
}

func (f *fakeDocker) last() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1]
}

func TestReviewRunLogs(t *testing.T) {
	r := newReviewRig(t)
	dock := &fakeDocker{out: "ok\n", exit: 0}
	r.srv.SetReviewRunner(review.Runner{Docker: dock.run, UID: 1000, GID: 1000})
	r.srv.SetReviewConfig(config.ReviewConfig{}, map[string]config.RepoBootstrap{
		"o/r": {Review: config.RepoReview{Image: "golang:1.26"}},
	})
	info := createReview(t, r.srv)

	var res struct {
		Type   string `json:"type"`
		Output string `json:"output"`
		Exit   int    `json:"exit"`
		N      int    `json:"n"`
	}
	rpcOK(t, r.srv, info.ID, "review.run", map[string]any{
		"cmd": []string{"go", "test", "./..."},
	}, &res)
	if res.Output != "ok\n" || res.Exit != 0 || res.N != 1 {
		t.Fatalf("result %+v", res)
	}
	logPath := filepath.Join(r.stateDir, info.ID, "work", ".aiman_review", "runs", "1.log")
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "go test ./...") || !strings.Contains(text, "exit: 0") || !strings.Contains(text, "ok") {
		t.Fatalf("log %q", text)
	}
	var n, exit any
	for _, ev := range ledger(t, r.srv, info.ID) {
		if ev.Kind == domain.EventReviewRun {
			var p map[string]any
			_ = json.Unmarshal(ev.Payload, &p)
			n, exit = p["n"], p["exit"]
		}
	}
	if n != float64(1) || exit != float64(0) {
		t.Fatalf("ledger n=%v exit=%v", n, exit)
	}
}

func TestStopReviewDownsSandbox(t *testing.T) {
	r := newReviewRig(t)
	dock := &fakeDocker{}
	r.srv.SetReviewRunner(review.Runner{Docker: dock.run, UID: 1000, GID: 1000})
	info := createReview(t, r.srv)
	sess, err := r.srv.repo.Get(context.Background(), info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := (serverTerminal{r.srv}).Stop(context.Background(), *sess); err != nil {
		t.Fatal(err)
	}
	args := dock.last()
	compose := filepath.Join(r.stateDir, info.ID, "sandbox", "compose.yaml")
	wantPrefix := []string{"compose", "-f", compose, "down", "-v"}
	if len(args) < 5 || strings.Join(args[:5], " ") != strings.Join(wantPrefix, " ") {
		t.Fatalf("down args %q, want prefix %q", args, wantPrefix)
	}
}

func TestCallDeadlineReviewRun(t *testing.T) {
	if got := callDeadline("review.run"); got <= ReviewRunTimeout {
		t.Fatalf("review.run deadline %v, want more than %v", got, ReviewRunTimeout)
	}
	if got := callDeadline("review.run"); got != ReviewRunTimeout+15*time.Second {
		t.Fatalf("review.run deadline %v", got)
	}
}
