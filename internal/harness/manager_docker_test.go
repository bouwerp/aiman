package harness

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

const dockerManagerFile = `version: 1
app:
  build: echo built > /out/built
  run: echo ok > /tmp/index.html && exec httpd -f -p 8080 -h /tmp
  ports: [{name: web, container: 8080}]
scenarios:
  - id: plain
    setup: [{run: test -f /src/.aiman/harness.yaml}]
    steps: [{do: "Open {{web}}"}]
    checks:
      - {name: built, run: test -f /out/built}
      - {name: offline, run: "! wget -q -T 3 -O- http://1.1.1.1/"}
`

// TestManagerDockerUpDown brings a busybox scenario up for real, reaches the
// app through the edge relay, runs its checks and takes it down.
func TestManagerDockerUpDown(t *testing.T) {
	needDocker(t)
	ensureImage(t, dockerTestImage)
	ensureImage(t, EdgeImage)
	repo := testRepo(t, dockerManagerFile)
	rec := &recorder{}
	m, err := NewManager(Options{
		Dir:      filepath.Join(t.TempDir(), "harness"),
		AimanBin: edgeBinary(t),
		UID:      os.Getuid(), GID: os.Getgid(),
		AllowedImages: []string{"busybox:*"},
		Docker:        ExecDocker{},
		AppImage:      func(string, string) (string, error) { return dockerTestImage, nil },
		Record:        rec.record,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	run, err := m.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "plain", Actor: domain.EventActorUser})
	if err != nil {
		t.Fatal(err)
	}
	project := "harness-" + run.ID
	t.Cleanup(func() {
		_ = exec.Command("docker", "compose", "-p", project, "down", "-v", "--remove-orphans", "-t", "1").Run()
	})
	run = m.Wait(context.Background(), run.ID, 3*time.Minute)
	if run.State != StateReady {
		t.Fatalf("run %s: %s: %s", run.State, run.FailedStep, run.Error)
	}
	if len(run.Ports) != 1 || run.Ports[0].HostPort == 0 {
		t.Fatalf("ports = %+v", run.Ports)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(run.Ports[0].HostPort)+"/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("relay: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("relay answered %q", body)
	}
	checks, err := m.Check(context.Background(), run.ID, "", domain.EventActorAgent)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range checks {
		if c.Exit == nil || *c.Exit != 0 {
			t.Errorf("check %s exit %v: %s", c.Name, c.Exit, c.Output)
		}
	}
	if err := m.Down(context.Background(), run.ID, StopDown); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "compose", "-p", project, "ps", "-a", "-q").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("containers left after down: %v %s", err, out)
	}
	if lines, err := m.Logs(context.Background(), run.ID, ServiceEdge, 50); err != nil || len(lines) == 0 {
		t.Fatalf("saved edge log: %v %q", err, lines)
	}
}
