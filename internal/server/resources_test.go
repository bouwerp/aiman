package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/cgroup"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
)

func resourceServer(t *testing.T, rows ...domain.Session) (*Server, ResourceOptions) {
	t.Helper()
	srv, _ := rulesServer(t, rows)
	dir := t.TempDir()
	opts := ResourceOptions{
		Limits:    cgroup.Limits{MemoryHigh: 6 << 30, MemoryMax: 10 << 30, TasksMax: 4096},
		Overrides: &cgroup.Overrides{Dir: filepath.Join(dir, "limits")},
		TmpRoot:   filepath.Join(dir, "tmp"),
	}
	srv.SetResources(opts)
	return srv, opts
}

// A session's terminal gets the session's scope, limits and scratch
// directory; a helper terminal named after it shares all three.
func TestPTYCreateUsesSessionScopeLimitsAndTmpdir(t *testing.T) {
	srv, opts := resourceServer(t, rulesRow("a", ""), rulesRow("a-b", ""))
	if _, err := opts.Overrides.Set("a", cgroup.Limits{MemoryHigh: 12 << 30}); err != nil {
		t.Fatal(err)
	}
	pty := srv.pty.(interface {
		Creates() []ptyruntime.Spec
	})
	for _, c := range []struct {
		id, key string
		high    int64
		max     int64
	}{
		{"a", "a", 12 << 30, 12 << 30},
		{"a-run", "a", 12 << 30, 12 << 30},
		{"a-b", "a-b", 6 << 30, 10 << 30},
		{"a-b-run", "a-b", 6 << 30, 10 << 30},
		{"lonely", "lonely", 6 << 30, 10 << 30},
	} {
		resp := rulesCall(t, srv, "", "pty.create", map[string]any{
			"id": c.id, "command": "bash", "env": map[string]string{"KEEP": "1"},
		})
		if resp.Error != nil {
			t.Fatalf("pty.create %s: %+v", c.id, resp.Error)
		}
		creates := pty.Creates()
		spec := creates[len(creates)-1]
		if spec.ScopeKey != c.key || spec.Limits.MemoryHigh != c.high || spec.Limits.MemoryMax != c.max || spec.Limits.TasksMax != 4096 {
			t.Errorf("%s: scope %q limits %+v; want scope %q high %d max %d", c.id, spec.ScopeKey, spec.Limits, c.key, c.high, c.max)
		}
		want := filepath.Join(opts.TmpRoot, c.key)
		if spec.Env["TMPDIR"] != want || spec.Env["CLAUDE_CODE_TMPDIR"] != want || spec.Env["KEEP"] != "1" {
			t.Errorf("%s: env %v; want TMPDIR %s", c.id, spec.Env, want)
		}
		if st, err := os.Stat(want); err != nil || st.Mode().Perm() != 0o700 {
			t.Errorf("%s: tmpdir %s: %v, %v", c.id, want, st, err)
		}
	}
}

func TestPTYCreateKeepsCallerTmpdirAndHonoursTmpdirOff(t *testing.T) {
	srv, _ := resourceServer(t, rulesRow("a", ""))
	pty := srv.pty.(interface {
		Creates() []ptyruntime.Spec
	})
	rulesCall(t, srv, "", "pty.create", map[string]any{"id": "a", "command": "bash", "env": map[string]string{"TMPDIR": "/mine"}})
	if got := pty.Creates()[0].Env["TMPDIR"]; got != "/mine" {
		t.Fatalf("caller's TMPDIR replaced with %q", got)
	}
	srv.SetResources(ResourceOptions{})
	rulesCall(t, srv, "", "pty.create", map[string]any{"id": "b", "command": "bash"})
	if env := pty.Creates()[1].Env; env["TMPDIR"] != "" {
		t.Fatalf("tmpdir off still set %v", env)
	}
}

// The scratch directory and the override go when the session is forgotten,
// never when a helper terminal of a live session is.
func TestForgetRemovesSessionTmpdirAndOverride(t *testing.T) {
	srv, opts := resourceServer(t, rulesRow("a", ""))
	for _, id := range []string{"a", "a-run", "lonely"} {
		if resp := rulesCall(t, srv, "", "pty.create", map[string]any{"id": id, "command": "bash"}); resp.Error != nil {
			t.Fatalf("create %s: %+v", id, resp.Error)
		}
	}
	if _, err := opts.Overrides.Set("a", cgroup.Limits{MemoryHigh: 12 << 30}); err != nil {
		t.Fatal(err)
	}
	exists := func(id string) bool {
		_, err := os.Stat(filepath.Join(opts.TmpRoot, id))
		return err == nil
	}

	rulesCall(t, srv, "", "pty.forget", map[string]any{"id": "a-run"})
	if !exists("a") {
		t.Fatal("forgetting a helper terminal removed its session's tmpdir")
	}
	rulesCall(t, srv, "", "pty.forget", map[string]any{"id": "lonely"})
	if exists("lonely") {
		t.Fatal("forgetting a terminal with no session left its tmpdir")
	}
	if resp := rulesCall(t, srv, "", "session.forget", map[string]any{"id": "a"}); resp.Error != nil {
		t.Fatalf("session.forget: %+v", resp.Error)
	}
	if exists("a") {
		t.Fatal("session.forget left the session's tmpdir")
	}
	if opts.Overrides.Get("a") != (cgroup.Limits{}) {
		t.Fatal("session.forget left the session's limit override")
	}
}

func TestSessionTmpDirRefusesPathIDs(t *testing.T) {
	o := ResourceOptions{TmpRoot: "/x/tmp"}
	for _, id := range []string{"", ".", "..", "a/b", `a\b`} {
		if got := o.sessionTmpDir(id); got != "" {
			t.Errorf("sessionTmpDir(%q) = %q", id, got)
		}
	}
}
