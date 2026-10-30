package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
	"github.com/bouwerp/aiman/internal/review"
)

func TestDebugRegistersTunnels(t *testing.T) {
	r := newReviewRig(t)
	pty := ptyfake.New()
	r.srv.pty = pty
	dock := &fakeDocker{out: "ok\n"}
	r.srv.SetReviewRunner(review.Runner{Docker: dock.run, UID: 1000, GID: 1000})
	r.srv.SetReviewConfig(config.ReviewConfig{}, map[string]config.RepoBootstrap{
		"o/r": {Review: config.RepoReview{Image: "golang:1.26"}},
	})
	info := createReview(t, r.srv)
	orderDir := filepath.Join(r.stateDir, info.ID, "work", ".aiman_review")
	if err := os.MkdirAll(orderDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orderDir, "reading-order.txt"), []byte("pkg/a.go:4\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var res struct {
		Type    string `json:"type"`
		Session SessionInfo
	}
	rpcOK(t, r.srv, info.ID, "review.debug", map[string]any{"lang": "go"}, &res)
	if res.Type != "review_debug" {
		t.Fatalf("type %q", res.Type)
	}

	saved, err := r.srv.repo.Get(context.Background(), info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Tunnels) == 0 {
		t.Fatal("no tunnels on the session")
	}
	for _, tun := range saved.Tunnels {
		if tun.LocalPort <= 0 || tun.RemotePort != tun.LocalPort {
			t.Fatalf("tunnel %+v: want LocalPort=RemotePort>0", tun)
		}
	}
	if len(res.Session.Tunnels) == 0 {
		t.Fatal("session.get wire lost tunnels")
	}

	creates := pty.Creates()
	if len(creates) != 1 {
		t.Fatalf("pty creates %d, want 1", len(creates))
	}
	spec := creates[0]
	if spec.Name != "debug" || spec.ParentID != info.ID || spec.Command == "" {
		t.Fatalf("spec %+v, want name=debug parent=%s", spec, info.ID)
	}
	if !strings.Contains(spec.Command, "--service-ports") {
		t.Fatalf("command %q missing --service-ports", spec.Command)
	}

	launch := filepath.Join(info.WorktreePath, ".vscode", "launch.json")
	if _, err := os.Stat(launch); err != nil {
		t.Fatalf("launch.json: %v", err)
	}

	debugID := "debug-" + info.ID
	if _, err := pty.Get(debugID); err != nil {
		t.Fatalf("debug helper missing after review.debug: %v", err)
	}
	killsBefore := countID(pty.Kills(), debugID)
	forgetsBefore := countID(pty.Forgets(), debugID)
	rpcOK(t, r.srv, info.ID, "session.forget", map[string]any{"id": info.ID}, nil)
	if _, err := pty.Get(debugID); err == nil {
		t.Fatal("debug helper still live after session.forget")
	}
	if countID(pty.Kills(), debugID) <= killsBefore {
		t.Fatalf("drop did not Kill %s: %v", debugID, pty.Kills())
	}
	if countID(pty.Forgets(), debugID) <= forgetsBefore {
		t.Fatalf("drop did not Forget %s: %v", debugID, pty.Forgets())
	}
}

func TestDebugLaunchJSONFailureLeavesNoHelper(t *testing.T) {
	r := newReviewRig(t)
	pty := ptyfake.New()
	r.srv.pty = pty
	r.srv.SetReviewRunner(review.Runner{Docker: (&fakeDocker{out: "ok\n"}).run, UID: 1000, GID: 1000})
	r.srv.SetReviewConfig(config.ReviewConfig{}, map[string]config.RepoBootstrap{
		"o/r": {Review: config.RepoReview{Image: "golang:1.26"}},
	})
	info := createReview(t, r.srv)
	vscode := filepath.Join(info.WorktreePath, ".vscode")
	if err := os.MkdirAll(vscode, 0o700); err != nil {
		t.Fatal(err)
	}
	jsonc := []byte("{\n  // comment\n  \"version\": \"0.2.0\",\n  \"configurations\": []\n}\n")
	if err := os.WriteFile(filepath.Join(vscode, "launch.json"), jsonc, 0o600); err != nil {
		t.Fatal(err)
	}

	resp := rpc(t, r.srv, info.ID, "review.debug", map[string]any{"lang": "go"})
	if resp.Error == nil || resp.Error.Code != CodeCreateFailed {
		t.Fatalf("want create_failed, got %+v", resp.Error)
	}
	debugID := "debug-" + info.ID
	if _, err := pty.Get(debugID); err == nil {
		t.Fatal("debug helper still live after launch.json failure")
	}
}

func countID(ids []string, want string) int {
	n := 0
	for _, id := range ids {
		if id == want {
			n++
		}
	}
	return n
}
