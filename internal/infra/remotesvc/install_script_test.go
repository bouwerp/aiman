package remotesvc

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// installBinaryFn is install.sh's install_binary, run on its own so no test
// downloads anything.
func installBinaryFn(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	fn := regexp.MustCompile(`(?s)\ninstall_binary\(\) \{.*?\n\}\n`).Find(raw)
	if fn == nil {
		t.Fatal("no install_binary in install.sh")
	}
	return string(fn)
}

// runInstallBinary installs a new binary over an old one with the given PATH
// and returns the combined output and error.
func runInstallBinary(t *testing.T, path string) (dest string, out []byte, err error) {
	t.Helper()
	src, target := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "aiman-gateway"), []byte("new binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest = filepath.Join(target, "aiman-gateway")
	if err := os.WriteFile(dest, []byte("old binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "set -e\nBINARY_NAME=aiman-gateway\nINSTALL_DIR=" + target + "\nensure_path() { :; }\n" + installBinaryFn(t) + "install_binary\n"
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "PATH="+path)
	out, err = cmd.CombinedOutput()
	return dest, out, err
}

func TestInstallBinaryReplacesTheTarget(t *testing.T) {
	dest, out, err := runInstallBinary(t, os.Getenv("PATH"))
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Fatalf("target %q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("left beside the target: %v", entries)
	}
}

// A copy killed part way writes some bytes and fails. The old binary must stay
// whole, with no partial file left beside it, and the install must fail.
func TestInstallBinaryKilledCopyKeepsTheOldBinary(t *testing.T) {
	bin := t.TempDir()
	fakeCP := "#!/bin/sh\nprintf partial > \"$2\"\nkill -TERM $$\n"
	if err := os.WriteFile(filepath.Join(bin, "cp"), []byte(fakeCP), 0o700); err != nil { // #nosec G306 -- the fake cp must be executable
		t.Fatal(err)
	}
	dest, out, err := runInstallBinary(t, bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil {
		t.Fatalf("install succeeded after a killed copy:\n%s", out)
	}
	got, readErr := os.ReadFile(dest)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old binary" {
		t.Fatalf("target %q, want the old binary", got)
	}
	entries, readErr := os.ReadDir(filepath.Dir(dest))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 {
		t.Fatalf("left beside the target: %v", entries)
	}
}
