package design

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// realRun runs git for real and fails every other command, as gh is
// unavailable in tests.
func realRun(calls *[]string) func(context.Context, string, string, ...string) (string, error) {
	return func(ctx context.Context, dir, name string, args ...string) (string, error) {
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		if name != "git" {
			return "", errors.New("no " + name)
		}
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
}

func bareRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "docs.git")
	git(t, root, "init", "--bare", "-b", "main", bare)
	seed := filepath.Join(root, "seed")
	git(t, root, "clone", bare, seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("docs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "init")
	git(t, seed, "push", "origin", "HEAD:main")
	return bare
}

func writeSrc(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "design.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func newPub(t *testing.T, bare string, calls *[]string) *Publisher {
	return &Publisher{
		Cfg:  config.DesignConfig{Repo: bare, Site: "https://docs.example.com/"},
		Home: t.TempDir(),
		Run:  realRun(calls),
	}
}

func TestPublishFirstRevision(t *testing.T) {
	bare := bareRepo(t)
	var calls []string
	p := newPub(t, bare, &calls)
	res, err := p.Publish(context.Background(), writeSrc(t, "# one\n"), "r", "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != "docs/designs/r/PROJ-1.md" || res.Revision != 1 || res.Local {
		t.Fatalf("result %+v", res)
	}
	if res.URL != "https://docs.example.com/designs/r/PROJ-1.html" {
		t.Fatalf("url %q", res.URL)
	}
	check := filepath.Join(t.TempDir(), "check")
	git(t, filepath.Dir(check), "clone", bare, check)
	if got := git(t, check, "log", "-1", "--format=%s"); got != "design(r): PROJ-1 rev 1" {
		t.Fatalf("subject %q", got)
	}
	if b, err := os.ReadFile(filepath.Join(check, res.Path)); err != nil || string(b) != "# one\n" {
		t.Fatalf("content %q %v", b, err)
	}
}

func TestPublishSecondRevision(t *testing.T) {
	bare := bareRepo(t)
	var calls []string
	p := newPub(t, bare, &calls)
	ctx := context.Background()
	if _, err := p.Publish(ctx, writeSrc(t, "# one\n"), "r", "PROJ-1"); err != nil {
		t.Fatal(err)
	}
	res, err := p.Publish(ctx, writeSrc(t, "# two\n"), "r", "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Revision != 2 {
		t.Fatalf("revision %d", res.Revision)
	}
	check := filepath.Join(t.TempDir(), "check")
	git(t, filepath.Dir(check), "clone", bare, check)
	if got := git(t, check, "log", "-1", "--format=%s"); got != "design(r): PROJ-1 rev 2" {
		t.Fatalf("subject %q", got)
	}
}

func TestPublishUnchangedKeepsRevision(t *testing.T) {
	bare := bareRepo(t)
	var calls []string
	p := newPub(t, bare, &calls)
	ctx := context.Background()
	src := writeSrc(t, "# one\n")
	if _, err := p.Publish(ctx, src, "r", "PROJ-1"); err != nil {
		t.Fatal(err)
	}
	res, err := p.Publish(ctx, src, "r", "PROJ-1")
	if err != nil || res.Revision != 1 {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestPublishRebasesOnMovedMain(t *testing.T) {
	bare := bareRepo(t)
	var calls []string
	p := newPub(t, bare, &calls)
	ctx := context.Background()
	if _, err := p.Publish(ctx, writeSrc(t, "# one\n"), "r", "PROJ-1"); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "clone", bare, other)
	if err := os.WriteFile(filepath.Join(other, "NOTES.md"), []byte("n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, other, "add", ".")
	git(t, other, "commit", "-m", "other work")
	git(t, other, "push", "origin", "HEAD:main")

	res, err := p.Publish(ctx, writeSrc(t, "# two\n"), "r", "PROJ-2")
	if err != nil || res.Revision != 1 {
		t.Fatalf("res %+v err %v", res, err)
	}
	git(t, other, "pull", "--rebase", "origin", "main")
	log := git(t, other, "log", "--format=%s")
	for _, want := range []string{"other work", "design(r): PROJ-1 rev 1", "design(r): PROJ-2 rev 1"} {
		if !strings.Contains(log, want) {
			t.Fatalf("log missing %q:\n%s", want, log)
		}
	}
}

func TestPublishLocalWhenUnset(t *testing.T) {
	var calls []string
	gitDir := filepath.Join(t.TempDir(), "gitdir")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := &Publisher{Home: t.TempDir(), Run: realRun(&calls)}
	res, err := p.PublishBytes(context.Background(), []byte("# one\n"), gitDir, "r", "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(gitDir, "aiman-design", "PROJ-1.md")
	if !res.Local || res.Path != want || res.Revision != 0 {
		t.Fatalf("result %+v", res)
	}
	if b, err := os.ReadFile(want); err != nil || string(b) != "# one\n" {
		t.Fatalf("content %q %v", b, err)
	}
	if len(calls) != 0 {
		t.Fatalf("calls %v", calls)
	}
}

func TestPublishRecoversDirtyCheckout(t *testing.T) {
	bare := bareRepo(t)
	var calls []string
	p := newPub(t, bare, &calls)
	ctx := context.Background()
	if _, err := p.Publish(ctx, writeSrc(t, "# one\n"), "r", "PROJ-1"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(p.Home, checkoutRel)
	// An unpushed commit for the same page, plus a staged change and junk.
	if err := os.WriteFile(filepath.Join(dir, "docs/designs/r/PROJ-1.md"), []byte("# stray\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "commit", "-am", "design(r): PROJ-1 rev 2")
	if err := os.WriteFile(filepath.Join(dir, "docs/designs/r/PROJ-1.md"), []byte("# staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "junk.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")

	res, err := p.Publish(ctx, writeSrc(t, "# two\n"), "r", "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Revision != 2 {
		t.Fatalf("revision %d, want 2", res.Revision)
	}
	check := filepath.Join(t.TempDir(), "check")
	git(t, filepath.Dir(check), "clone", bare, check)
	if _, err := os.Stat(filepath.Join(check, "junk.txt")); err == nil {
		t.Fatal("junk was pushed")
	}
}

func TestPublishRejectsDotNames(t *testing.T) {
	p := &Publisher{Home: t.TempDir()}
	for _, repo := range []string{".", "..", "a/b", ""} {
		if _, err := p.PublishBytes(context.Background(), []byte("x"), "", repo, "K-1"); !errors.Is(err, ErrInvalid) {
			t.Errorf("repo %q: err %v, want ErrInvalid", repo, err)
		}
	}
}

// visibilityRun answers gh api repos/<slug> with vis and fails the clone, so
// a publish stops right after the visibility check.
func visibilityRun(calls *[]string, vis string) func(context.Context, string, string, ...string) (string, error) {
	return func(_ context.Context, _ string, name string, args ...string) (string, error) {
		call := name + " " + strings.Join(args, " ")
		*calls = append(*calls, call)
		if strings.HasPrefix(call, "gh api repos/") && strings.HasSuffix(call, "-q .visibility") {
			return vis, nil
		}
		return "", errors.New("no clone in tests")
	}
}

func countPrefix(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestPublishRefusesPublicRepo(t *testing.T) {
	for _, repo := range []string{"acme/docs", "https://github.com/acme/docs.git", "git@github.com:acme/docs.git"} {
		var calls []string
		p := &Publisher{Cfg: config.DesignConfig{Repo: repo}, Home: t.TempDir(), Run: visibilityRun(&calls, "public")}
		_, err := p.PublishBytes(context.Background(), []byte("x"), "", "r", "K-1")
		if err == nil || !strings.Contains(err.Error(), "public") {
			t.Fatalf("%s: err %v, want a public-repo refusal", repo, err)
		}
		if countPrefix(calls, "gh api repos/acme/docs -q .visibility") != 1 || countPrefix(calls, "gh repo clone")+countPrefix(calls, "git clone") != 0 {
			t.Fatalf("%s: calls %v, want one visibility check and no clone", repo, calls)
		}
	}
}

func TestPublishChecksVisibilityOnce(t *testing.T) {
	for _, vis := range []string{"private", "internal"} {
		var calls []string
		p := &Publisher{Cfg: config.DesignConfig{Repo: "acme/docs"}, Home: t.TempDir(), Run: visibilityRun(&calls, vis)}
		for range 2 {
			_, err := p.PublishBytes(context.Background(), []byte("x"), "", "r", "K-1")
			if err == nil || !strings.Contains(err.Error(), "clone") {
				t.Fatalf("%s: err %v, want the clone failure", vis, err)
			}
		}
		if n := countPrefix(calls, "gh api repos/acme/docs -q .visibility"); n != 1 {
			t.Fatalf("%s: %d visibility checks, want 1: %v", vis, n, calls)
		}
	}
}

func TestPublishSkipsVisibilityForPath(t *testing.T) {
	var calls []string
	p := newPub(t, bareRepo(t), &calls)
	if _, err := p.Publish(context.Background(), writeSrc(t, "# one\n"), "r", "PROJ-1"); err != nil {
		t.Fatal(err)
	}
	if n := countPrefix(calls, "gh api repos/") - countPrefix(calls, "gh api repos/"+p.Cfg.Repo+"/pages"); n != 0 {
		t.Fatalf("visibility checked for a filesystem repo: %v", calls)
	}
}

func TestLockFileHonoursContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	unlock, err := lockFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := lockFile(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v, want a deadline error", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("lock wait ignored the context")
	}
}

func TestExecRunDisablesPromptsAndHonoursContext(t *testing.T) {
	out, err := ExecRun(context.Background(), t.TempDir(), "sh", "-c", "echo $GIT_TERMINAL_PROMPT")
	if err != nil || out != "0" {
		t.Fatalf("GIT_TERMINAL_PROMPT = %q, %v; want 0", out, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := ExecRun(ctx, t.TempDir(), "sleep", "30"); err == nil {
		t.Fatal("a cancelled run succeeded")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("run ignored the context")
	}
}

func TestCheckPrivateByLocation(t *testing.T) {
	tests := []struct {
		repo     string
		wantSlug string // gh is asked about this slug; empty means no gh call
		refused  bool
	}{
		{repo: "acme/docs", wantSlug: "acme/docs"},
		{repo: "acme/docs.git", wantSlug: "acme/docs"},
		{repo: "https://github.com/acme/docs", wantSlug: "acme/docs"},
		{repo: "https://GitHub.com/acme/docs.git", wantSlug: "acme/docs"},
		{repo: "https://www.github.com/acme/docs", wantSlug: "acme/docs"},
		{repo: "HTTPS://WWW.GITHUB.COM/acme/docs/", wantSlug: "acme/docs"},
		{repo: "ssh://git@github.com/acme/docs.git", wantSlug: "acme/docs"},
		{repo: "ssh://github.com/acme/docs", wantSlug: "acme/docs"},
		{repo: "ssh://git@www.github.com/acme/docs", wantSlug: "acme/docs"},
		{repo: "git@github.com:acme/docs.git", wantSlug: "acme/docs"},
		{repo: "git@GitHub.com:acme/docs", wantSlug: "acme/docs"},
		{repo: "git@www.github.com:acme/docs", wantSlug: "acme/docs"},
		{repo: "/srv/git/docs.git"},
		{repo: "./docs.git"},
		{repo: "../docs.git"},
		{repo: "file:///srv/git/docs.git"},
		{repo: "FILE:///srv/git/docs.git"},
		{repo: "https://gitlab.com/acme/docs.git", refused: true},
		{repo: "ssh://git@example.com/acme/docs.git", refused: true},
		{repo: "git@example.com:acme/docs.git", refused: true},
		{repo: "https://github.example.com/acme/docs.git", refused: true},
		{repo: "git@github.example.com:acme/docs.git", refused: true},
		{repo: "https://github.com.evil.io/acme/docs", refused: true},
		{repo: "https://github.com/acme", refused: true},
		{repo: "docs.git", refused: true},
		{repo: "a/b/c", refused: true},
	}
	for _, tt := range tests {
		t.Run(tt.repo, func(t *testing.T) {
			var calls []string
			p := &Publisher{Cfg: config.DesignConfig{Repo: tt.repo}, Run: visibilityRun(&calls, "private")}
			err := p.checkPrivate(context.Background())
			if tt.refused {
				if err == nil || !strings.Contains(err.Error(), tt.repo) || !strings.Contains(err.Error(), "visibility cannot be checked") {
					t.Fatalf("err %v, want a refusal naming the location", err)
				}
				if len(calls) != 0 {
					t.Fatalf("calls %v, want none", calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if tt.wantSlug != "" {
				want = 1
			}
			if n := countPrefix(calls, "gh api repos/"+tt.wantSlug+" -q .visibility"); n != want || len(calls) != want {
				t.Fatalf("calls %v, want %d check of %q", calls, want, tt.wantSlug)
			}
		})
	}
}
