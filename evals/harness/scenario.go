package harness

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	scenario "github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/edge"
	"github.com/bouwerp/aiman/internal/server"
)

// FakeDocker is a Docker engine for the scenario harness: it holds a fixed set
// of images, answers `port` with one host port, `run` (a check) with exit 0
// and "ok", and every other compose command with success. It runs nothing.
type FakeDocker struct {
	mu     sync.Mutex
	images map[string]string
	calls  []scenario.ComposeCall
}

// FakeDockerHostPort is the host port FakeDocker publishes for every port.
const FakeDockerHostPort = 41873

// NewFakeDocker holds refs as local images.
func NewFakeDocker(refs ...string) *FakeDocker {
	d := &FakeDocker{images: map[string]string{}}
	for i, ref := range refs {
		d.images[ref] = fmt.Sprintf("sha256:%064x", i+1)
	}
	return d
}

func (d *FakeDocker) ImageID(_ context.Context, ref string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id, ok := d.images[ref]; ok {
		return id, nil
	}
	return "", scenario.ErrImageNotFound
}

func (d *FakeDocker) Compose(_ context.Context, call scenario.ComposeCall) (string, int, error) {
	d.mu.Lock()
	call.Args = append([]string(nil), call.Args...)
	call.Stdin = nil
	d.calls = append(d.calls, call)
	d.mu.Unlock()
	if len(call.Args) == 0 {
		return "", 0, nil
	}
	switch call.Args[0] {
	case "port":
		return fmt.Sprintf("127.0.0.1:%d\n", FakeDockerHostPort), 0, nil
	case "run":
		return "ok\n", 0, nil
	}
	return "", 0, nil
}

// ComposeArgs is every compose command received, its arguments joined by
// spaces.
func (d *FakeDocker) ComposeArgs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.calls))
	for _, c := range d.calls {
		out = append(out, strings.Join(c.Args, " "))
	}
	return out
}

// fakeEdge reports every run's edge healthy.
type fakeEdge struct{}

func (fakeEdge) Health(context.Context, string, string) (edge.Health, error) {
	return edge.Health{}, nil
}

// FakeDockerImages are the images a scenario harness eval holds: the app's
// image, a service image and the edge.
var FakeDockerImages = []string{"golang:1.25", "postgres:16", scenario.EdgeImage}

// EnableScenarioHarness turns on serve's harness.* methods over d. The app
// image is always golang:1.25, and the manager's runs live under the harness
// directory. The manager stops with the case.
func (h *Harness) EnableScenarioHarness(d scenario.Docker) error {
	m, err := scenario.NewManager(scenario.Options{
		Dir:      filepath.Join(h.Dir, "scenario-harness"),
		AimanBin: "/usr/local/bin/aiman",
		UID:      1000, GID: 1000,
		Docker:   d,
		Edge:     fakeEdge{},
		AppImage: func(string, string) (string, error) { return "golang:1.25", nil },
		Record:   h.srv.HarnessRecorder(),
	})
	if err != nil {
		return err
	}
	h.t.Cleanup(m.Close)
	h.srv.SetHarness(server.HarnessDeps{Manager: m})
	return nil
}
