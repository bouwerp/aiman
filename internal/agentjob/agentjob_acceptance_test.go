//go:build feedaccept

package agentjob_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/agentjob"
)

var jobT0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

// fakeRunner records each container and answers with out or err. It moves
// the clock by took, so the job's duration is known.
type fakeRunner struct {
	runs  []agentjob.Container
	out   string
	err   error
	clock *time.Time
	took  time.Duration
}

func (f *fakeRunner) Run(_ context.Context, c agentjob.Container) (string, error) {
	f.runs = append(f.runs, c)
	if f.clock != nil {
		*f.clock = f.clock.Add(f.took)
	}
	return f.out, f.err
}

type jobHost struct {
	runner   *fakeRunner
	now      time.Time
	asked    []string
	minValid []time.Duration
	credErr  error
	opts     agentjob.Options
}

// newJobHost is a host with claude and codex installed as native binaries
// outside any job directory.
func newJobHost(t *testing.T) *jobHost {
	t.Helper()
	bins := t.TempDir()
	for _, name := range agentjob.Agents() {
		if err := os.WriteFile(filepath.Join(bins, name), []byte("\x7fELF fake"), 0o755); err != nil { //nolint:gosec // G306: a fake executable
			t.Fatal(err)
		}
	}
	h := &jobHost{now: jobT0}
	h.runner = &fakeRunner{out: "the reply", clock: &h.now, took: 7 * time.Second}
	h.opts = agentjob.Options{
		Runner: h.runner,
		Image:  func(context.Context) (string, error) { return "scanner:abc", nil },
		Credentials: func(agent string, minValid time.Duration) (agentjob.Credential, error) {
			h.asked = append(h.asked, agent)
			h.minValid = append(h.minValid, minValid)
			if h.credErr != nil {
				return agentjob.Credential{}, h.credErr
			}
			return agentjob.Credential{Mode: "oauth_copy", File: []byte(`{"token":"t"}`)}, nil
		},
		LookPath: func(command string) (string, error) {
			p := filepath.Join(bins, command)
			if _, err := os.Stat(p); err != nil {
				return "", err
			}
			return p, nil
		},
		AimanBin: "/usr/local/bin/aiman",
		UID:      1000,
		GID:      1000,
		Now:      func() time.Time { return h.now },
	}
	return h
}

func newJob(t *testing.T, agent, model string) agentjob.Job {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "batch.md"), []byte("events"), 0o600); err != nil {
		t.Fatal(err)
	}
	return agentjob.Job{Name: "aiman-feed-12", Agent: agent, Model: model, Dir: dir, Brief: "read /job/brief.md", Timeout: 3 * time.Minute}
}

func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func TestAgentsAreClaudeAndCodex(t *testing.T) {
	got := agentjob.Agents()
	slices.Sort(got)
	if !slices.Equal(got, []string{"claude", "codex"}) {
		t.Fatalf("Agents() = %v, want [claude codex]", got)
	}
}

func TestEntrypointAllowList(t *testing.T) {
	cases := []struct {
		agent, model, mode string
		allow              []string
		modelArgs          []string
	}{
		{"claude", "opus", "oauth_copy", []string{"api.anthropic.com:443"}, []string{"--model", "opus"}},
		{"claude", "", "api_key", []string{"api.anthropic.com:443"}, nil},
		{"codex", "gpt-6", "api_key", []string{"api.openai.com:443"}, []string{"-m", "gpt-6"}},
		{"codex", "", "oauth_copy", []string{"chatgpt.com:443"}, nil},
	}
	for _, c := range cases {
		t.Run(c.agent+"/"+c.mode, func(t *testing.T) {
			argv, allow, err := agentjob.Entrypoint(c.agent, c.model, agentjob.Credential{Mode: c.mode})
			if err != nil {
				t.Fatal(err)
			}
			if len(argv) == 0 {
				t.Fatal("empty argv")
			}
			if !slices.Equal(allow, c.allow) {
				t.Errorf("allow = %v, want %v", allow, c.allow)
			}
			if c.modelArgs != nil && !containsRun(argv, c.modelArgs) {
				t.Errorf("argv %q lacks %q", argv, c.modelArgs)
			}
		})
	}
	if _, _, err := agentjob.Entrypoint("pi", "", agentjob.Credential{}); err == nil {
		t.Error("Entrypoint(pi) succeeded, want an error for an agent the container cannot run")
	}
}

func containsRun(argv, want []string) bool {
	for i := 0; i+len(want) <= len(argv); i++ {
		if slices.Equal(argv[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

func TestRunBuildsTheContainer(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			h := newJobHost(t)
			j := newJob(t, agent, "m1")
			res, err := agentjob.Run(context.Background(), h.opts, j)
			if err != nil {
				t.Fatal(err)
			}
			if len(h.runner.runs) != 1 {
				t.Fatalf("runner ran %d times, want 1", len(h.runner.runs))
			}
			c := h.runner.runs[0]
			if c.Name != j.Name || c.Image != "scanner:abc" || c.UID != 1000 || c.GID != 1000 || c.AimanBin != "/usr/local/bin/aiman" {
				t.Errorf("container identity = %+v", c)
			}
			if c.Stdin != j.Brief {
				t.Errorf("Stdin = %q, want the brief", c.Stdin)
			}
			if c.Timeout != j.Timeout {
				t.Errorf("Timeout = %s, want %s", c.Timeout, j.Timeout)
			}
			_, allow, err := agentjob.Entrypoint(agent, "m1", agentjob.Credential{Mode: "oauth_copy"})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(c.Allow, allow) {
				t.Errorf("Allow = %v, want Entrypoint's %v", c.Allow, allow)
			}
			jobMounts := 0
			for _, m := range c.Mounts {
				if within(j.Dir, m.Host) {
					jobMounts++
					if m.Host != j.Dir || m.Container != "/job" {
						t.Errorf("job mount %+v, want %s at /job", m, j.Dir)
					}
				}
			}
			if jobMounts != 1 {
				t.Errorf("%d mounts from the job directory, want exactly 1: %+v", jobMounts, c.Mounts)
			}
			if c.EnvFile == "" || within(j.Dir, c.EnvFile) {
				t.Errorf("EnvFile %q must exist outside the job directory, which the agent reads", c.EnvFile)
			}
			if !slices.Equal(h.asked, []string{agent}) || h.minValid[0] < j.Timeout {
				t.Errorf("credentials asked %v valid for %v, want %s for at least %s", h.asked, h.minValid, agent, j.Timeout)
			}
			if res.Stdout != "the reply" || res.Agent != agent || res.Model != "m1" {
				t.Errorf("result = %+v", res)
			}
			if res.Duration != 7*time.Second {
				t.Errorf("Duration = %s, want 7s from the clock", res.Duration)
			}
		})
	}
}

func TestRunDefaultsTheTimeout(t *testing.T) {
	h := newJobHost(t)
	j := newJob(t, "claude", "")
	j.Timeout = 0
	if _, err := agentjob.Run(context.Background(), h.opts, j); err != nil {
		t.Fatal(err)
	}
	if len(h.runner.runs) != 1 {
		t.Fatalf("runner ran %d times, want 1", len(h.runner.runs))
	}
	if c := h.runner.runs[0]; c.Timeout <= 0 {
		t.Errorf("Timeout = %s, want a positive default", c.Timeout)
	}
}

func TestRunRefuses(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name   string
		setup  func(h *jobHost, j *agentjob.Job)
		runs   int
		target error
	}{
		{"unknown agent", func(_ *jobHost, j *agentjob.Job) { j.Agent = "pi" }, 0, nil},
		{"no job dir", func(_ *jobHost, j *agentjob.Job) { j.Dir = "" }, 0, nil},
		{"credentials", func(h *jobHost, _ *agentjob.Job) { h.credErr = boom }, 0, boom},
		{"image", func(h *jobHost, _ *agentjob.Job) {
			h.opts.Image = func(context.Context) (string, error) { return "", boom }
		}, 0, boom},
		{"runner", func(h *jobHost, _ *agentjob.Job) { h.runner.err = boom }, 1, boom},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newJobHost(t)
			j := newJob(t, "claude", "")
			c.setup(h, &j)
			_, err := agentjob.Run(context.Background(), h.opts, j)
			if err == nil {
				t.Fatal("Run succeeded, want an error")
			}
			if c.target != nil && !errors.Is(err, c.target) {
				t.Errorf("err = %v, want it to wrap %v", err, c.target)
			}
			if len(h.runner.runs) != c.runs {
				t.Errorf("runner ran %d times, want %d", len(h.runner.runs), c.runs)
			}
		})
	}
}
