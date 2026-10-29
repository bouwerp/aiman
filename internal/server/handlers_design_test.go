package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/design"
)

func TestDesignPublishLocalFallback(t *testing.T) {
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "d.md"), []byte("## Decisions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := todoSession("s1")
	sess.WorkingDirectory = wt
	sess.IssueKey = "PROJ-9"
	sess.RepoName = "acme/widgets"
	srv, _, _ := todoServer(t, sess)

	if resp := rpc(t, srv, "s1", "design.publish", map[string]any{"file": "d.md"}); resp.Error == nil {
		t.Fatal("expected error without a publisher")
	}
	srv.SetDesignPublisher(&design.Publisher{Home: t.TempDir(), Run: design.ExecRun})

	var out struct {
		Type   string        `json:"type"`
		Design design.Result `json:"design"`
	}
	rpcOK(t, srv, "s1", "design.publish", map[string]any{"file": "d.md"}, &out)
	want := filepath.Join(wt, ".git", "aiman-design", "PROJ-9.md")
	if out.Type != "design" || !out.Design.Local || out.Design.Path != want {
		t.Fatalf("result %+v", out)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
}

func TestRepoBase(t *testing.T) {
	for in, want := range map[string]string{"acme/widgets": "widgets", "solo": "solo", "": "repo"} {
		if got := repoBase(in); got != want {
			t.Errorf("repoBase(%q)=%q", in, got)
		}
	}
}

func TestDesignPublishConfinesFile(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "wt")
	outside := filepath.Join(root, "secret.yaml")
	for _, d := range []string{filepath.Join(wt, ".git")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(outside, []byte("token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(wt, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "ok.md"), []byte("ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess := todoSession("s1")
	sess.WorkingDirectory = wt
	sess.IssueKey = "PROJ-1"
	srv, _, _ := todoServer(t, sess)
	srv.SetDesignPublisher(&design.Publisher{Home: t.TempDir(), Run: design.ExecRun})

	for name, file := range map[string]string{"absolute": outside, "dotdot": "../secret.yaml", "symlink": "link.md"} {
		resp := rpc(t, srv, "s1", "design.publish", map[string]any{"file": file})
		if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
			t.Errorf("%s: want invalid_params, got %+v", name, resp)
		}
	}
	if resp := rpc(t, srv, "s1", "design.publish", map[string]any{"file": "ok.md"}); resp.Error != nil {
		t.Fatalf("inside file rejected: %v", resp.Error)
	}
}
