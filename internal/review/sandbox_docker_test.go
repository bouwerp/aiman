package review

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestSandboxRunsGoTestInDocker runs the generated compose file through real
// Docker. Fakes cannot see volume ownership or tmpfs mount options, so CI sets
// AIMAN_TEST_DOCKER=1 for this test.
func TestSandboxRunsGoTestInDocker(t *testing.T) {
	if os.Getenv("AIMAN_TEST_DOCKER") != "1" {
		t.Skip("set AIMAN_TEST_DOCKER=1 to run against real Docker")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker not on PATH: %v", err)
	}
	base := t.TempDir()
	sb := Sandbox{
		ID:    fmt.Sprintf("aiman-sandbox-test-%d", os.Getpid()),
		Dir:   filepath.Join(base, "sandbox"),
		Src:   filepath.Join(base, "src"),
		Out:   filepath.Join(base, "out"),
		Image: "golang:1.26",
	}
	for _, d := range []string{sb.Src, sb.Out} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"go.mod":    "module example.com/probe\n\ngo 1.26\n",
		"p.go":      "package probe\n\nfunc One() int { return 1 }\n",
		"p_test.go": "package probe\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {\n\tif One() != 1 {\n\t\tt.Fatal(\"one\")\n\t}\n}\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(sb.Src, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	r := Runner{Docker: DockerExec}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		if err := r.Down(cleanup, sb); err != nil {
			t.Logf("down: %v", err)
		}
	})
	out, code, err := r.Run(ctx, sb, []string{"go", "test", "-coverprofile=/out/cover.out", "./..."}, 0)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if code != 0 {
		t.Fatalf("go test exited %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(sb.Out, "cover.out")); err != nil {
		t.Fatalf("cover.out: %v", err)
	}
}
