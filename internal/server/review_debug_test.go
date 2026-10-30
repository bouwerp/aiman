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
}
