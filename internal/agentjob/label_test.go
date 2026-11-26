package agentjob_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/agentjob"
)

type recordRunner struct{ runs []agentjob.Container }

func (r *recordRunner) Run(_ context.Context, c agentjob.Container) (string, error) {
	r.runs = append(r.runs, c)
	return "", nil
}

func labelIn(args []string) string {
	if i := slices.Index(args, "--label"); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func TestOptionsLabelReachesContainerArgs(t *testing.T) {
	bins := t.TempDir()
	bin := filepath.Join(bins, "claude")
	if err := os.WriteFile(bin, []byte("\x7fELF fake"), 0o755); err != nil { //nolint:gosec // G306: a fake executable
		t.Fatal(err)
	}
	runner := &recordRunner{}
	o := agentjob.Options{
		Runner: runner,
		Image:  func(context.Context) (string, error) { return "scanner:abc", nil },
		Credentials: func(string, time.Duration) (agentjob.Credential, error) {
			return agentjob.Credential{Mode: "oauth_copy", File: []byte(`{"token":"t"}`)}, nil
		},
		LookPath: func(string) (string, error) { return bin, nil },
		AimanBin: "/usr/local/bin/aiman",
		Label:    "aiman.feed=1",
		What:     "feed job",
	}
	j := agentjob.Job{Name: "aiman-feed-1", Agent: "claude", Dir: t.TempDir(), Brief: "b"}
	if _, err := agentjob.Run(context.Background(), o, j); err != nil {
		t.Fatal(err)
	}
	if len(runner.runs) != 1 {
		t.Fatalf("runs = %d", len(runner.runs))
	}
	c := runner.runs[0]
	if c.Label != "aiman.feed=1" || c.What != "feed job" {
		t.Fatalf("container label %q what %q", c.Label, c.What)
	}
	if got := labelIn(agentjob.Args(c)); got != "aiman.feed=1" {
		t.Errorf("Args label = %q", got)
	}
	if got := labelIn(agentjob.ProxyArgs(c)); got != "aiman.feed=1" {
		t.Errorf("ProxyArgs label = %q", got)
	}
	c.Label = ""
	if got := labelIn(agentjob.Args(c)); got != agentjob.Label {
		t.Errorf("empty label: Args label = %q, want %q", got, agentjob.Label)
	}
}
