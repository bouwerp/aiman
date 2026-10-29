package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/design"
	"github.com/bouwerp/aiman/internal/infra/config"
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

// designServer is a session s1 whose working directory is a git checkout
// holding d.md and notes.txt.
func designServer(t *testing.T) *Server {
	t.Helper()
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"d.md": "## Decisions\n", "notes.txt": "x\n", "UP.MD": "# up\n"} {
		if err := os.WriteFile(filepath.Join(wt, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sess := todoSession("s1")
	sess.WorkingDirectory = wt
	sess.IssueKey = "PROJ-9"
	srv, _, _ := todoServer(t, sess)
	return srv
}

func TestDesignPublishOnlyMarkdown(t *testing.T) {
	srv := designServer(t)
	srv.SetDesignPublisher(&design.Publisher{Home: t.TempDir(), Run: design.ExecRun})
	resp := rpc(t, srv, "s1", "design.publish", map[string]any{"file": "notes.txt"})
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams || !strings.Contains(resp.Error.Message, ".md") {
		t.Fatalf("notes.txt: got %+v, want invalid_params naming .md", resp.Error)
	}
	if resp := rpc(t, srv, "s1", "design.publish", map[string]any{"file": "UP.MD"}); resp.Error != nil {
		t.Fatalf("UP.MD rejected: %+v", resp.Error)
	}
}

// A publish that fails after validation is publish_failed, and it runs under
// the publish deadline rather than the caller's.
func TestDesignPublishFailureCodeAndDeadline(t *testing.T) {
	srv := designServer(t)
	var deadline time.Time
	srv.SetDesignPublisher(&design.Publisher{
		Cfg:  config.DesignConfig{Repo: filepath.Join(t.TempDir(), "docs.git")},
		Home: t.TempDir(),
		Run: func(ctx context.Context, _, _ string, _ ...string) (string, error) {
			deadline, _ = ctx.Deadline()
			return "", errors.New("clone failed")
		},
	})
	resp := rpc(t, srv, "s1", "design.publish", map[string]any{"file": "d.md"})
	if resp.Error == nil || resp.Error.Code != CodePublishFailed {
		t.Fatalf("got %+v, want publish_failed", resp.Error)
	}
	left := time.Until(deadline)
	if left <= DesignPublishTimeout-time.Minute || left > DesignPublishTimeout {
		t.Fatalf("publish ran with %v left, want about %v", left, DesignPublishTimeout)
	}
}
