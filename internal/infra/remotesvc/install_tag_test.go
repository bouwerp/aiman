package remotesvc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testTag = "v0.29.3"

func TestReleaseTag(t *testing.T) {
	for version, want := range map[string]bool{
		"v0.29.3":            true,
		"dev":                false,
		"":                   false,
		"0.29.3":             false,
		"v0.29.2-3-gabc1234": false,
		"v0.29.3; rm -rf /":  false,
		"v0.29.3\nid":        false,
	} {
		if _, ok := ReleaseTag(version); ok != want {
			t.Errorf("ReleaseTag(%q) = %v, want %v", version, ok, want)
		}
	}
}

func TestInstallPipeAtInvalidTagFailsWithoutFetching(t *testing.T) {
	pipe := KindServe.InstallPipeAt("$(id)")
	if strings.Contains(pipe, "curl") || strings.Contains(pipe, "$(id)") {
		t.Fatalf("invalid tag reached the script: %q", pipe)
	}
	if err := exec.Command("sh", "-c", pipe).Run(); err == nil {
		t.Fatal("invalid tag command exited 0")
	}
}

func serveLatest(t *testing.T, status int, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	prev := latestReleaseURL
	latestReleaseURL = srv.URL
	t.Cleanup(func() { latestReleaseURL = prev })
}

func TestInstallTag(t *testing.T) {
	serveLatest(t, http.StatusOK, `{"tag_name":"v9.9.9"}`)
	ctx := context.Background()
	if tag, err := InstallTag(ctx, testTag); err != nil || tag != testTag {
		t.Fatalf("release client: %q %v", tag, err)
	}
	if tag, err := InstallTag(ctx, "dev"); err != nil || tag != "v9.9.9" {
		t.Fatalf("dev client: %q %v", tag, err)
	}
}

func TestLatestReleaseTagRefuses(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		body   string
	}{
		"rate limited": {http.StatusForbidden, `{}`},
		"no tag":       {http.StatusOK, `{}`},
		"odd tag":      {http.StatusOK, `{"tag_name":"v1.0.0; id"}`},
		"bad json":     {http.StatusOK, `<html>`},
	} {
		t.Run(name, func(t *testing.T) {
			serveLatest(t, c.status, c.body)
			if tag, err := LatestReleaseTag(context.Background()); err == nil {
				t.Fatalf("accepted %q", tag)
			}
		})
	}
}

// runInstallStep runs script under sh with curl stubbed by curlBody and only the
// tools it needs on PATH. It returns the exit error and whether the line after
// the install ran.
func runInstallStep(t *testing.T, script, curlBody string) (error, bool) {
	t.Helper()
	home, stubs := t.TempDir(), t.TempDir()
	writeExe(t, filepath.Join(stubs, "curl"), curlBody)
	for _, tool := range []string{"sh", "cat"} {
		real, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not available", tool)
		}
		if err := os.Symlink(real, filepath.Join(stubs, tool)); err != nil {
			t.Fatal(err)
		}
	}
	after := filepath.Join(home, "after")
	cmd := exec.Command("sh", "-c", script+"\necho ran > "+after+"\n")
	cmd.Env = []string{"HOME=" + home, "PATH=" + stubs}
	err := cmd.Run()
	_, statErr := os.Stat(after)
	return err, statErr == nil
}

// A download that fails must not read as a successful empty install, and a
// refusal from install.sh must stop what follows. Plain `curl | sh` exits 0 for
// the first and would restart the old binary.
func TestInstallPipeAtFailsVisibly(t *testing.T) {
	pipe := KindGateway.InstallPipeAt(testTag)
	cases := []struct {
		name string
		curl string
		ok   bool
	}{
		{"installer succeeds", "#!/bin/sh\necho 'exit 0'\n", true},
		{"release has no install.sh", "#!/bin/sh\nexit 22\n", false},
		{"install.sh refuses", "#!/bin/sh\necho 'exit 1'\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err, ran := runInstallStep(t, "set -e\n"+pipe, c.curl)
			if (err == nil) != c.ok || ran != c.ok {
				t.Fatalf("err %v, next step ran %v, want ok=%v", err, ran, c.ok)
			}
			// Without set -e the pipe itself still reports the failure.
			if err, _ := runInstallStep(t, pipe+" || exit 7", c.curl); (err == nil) != c.ok {
				t.Fatalf("no set -e: err %v, want ok=%v", err, c.ok)
			}
		})
	}
}

// install.sh reads VERSION and BINARY_NAME from the pipe's environment.
func TestInstallPipeAtPassesEnvToInstaller(t *testing.T) {
	home := t.TempDir()
	out := filepath.Join(home, "env")
	writeExe(t, filepath.Join(home, "stubs", "curl"),
		"#!/bin/sh\necho 'echo \"$VERSION $BINARY_NAME\" > "+out+"'\n")
	cmd := exec.Command("sh", "-c", KindTrigger.InstallPipeAt(testTag))
	cmd.Env = []string{"PATH=" + filepath.Join(home, "stubs") + ":" + os.Getenv("PATH")}
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != testTag+" aiman-trigger" {
		t.Fatalf("installer saw %q", got)
	}
}

// A refused update must not restart the old binary and report success.
func TestUpdateScriptStopsWhenInstallRefuses(t *testing.T) {
	script := UpdateScript(KindServe, testTag)
	if !strings.HasPrefix(script, "set -e\n"+KindServe.InstallPipeAt(testTag)) {
		t.Fatalf("install is not under set -e:\n%s", script)
	}
	home, stubs := t.TempDir(), t.TempDir()
	writeExe(t, filepath.Join(stubs, "curl"), "#!/bin/sh\necho 'exit 1'\n")
	for _, tool := range []string{"sh", "id", "mkdir", "cat", "nohup"} {
		real, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not available", tool)
		}
		if err := os.Symlink(real, filepath.Join(stubs, tool)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"HOME=" + home, "PATH=" + stubs}
	if err := cmd.Run(); err == nil {
		t.Fatal("update exited 0 after the install refused")
	}
	if _, err := os.Stat(filepath.Join(home, ".aiman", "serve.pid")); err == nil {
		t.Fatal("serve restarted after the install refused")
	}
}
