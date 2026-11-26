package agentjob

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// JobDir is where a job's directory is mounted read-only; the agent works
// there.
const JobDir = "/job"

// Job is one headless agent call.
type Job struct {
	// Name names the container and its network.
	Name  string
	Agent string
	Model string
	// Dir is the host directory mounted read-only at /job.
	Dir string
	// Brief goes to the agent on stdin.
	Brief string
	// Timeout bounds the agent run; zero is DefaultTimeout.
	Timeout time.Duration
}

// JobResult is the agent's stdout and what ran.
type JobResult struct {
	Stdout, Agent, Model string
	Duration             time.Duration
}

// Options are the host's seams for Run.
type Options struct {
	// Runner is nil for DockerRunner{}.
	Runner Runner
	// Image answers the container image (the scanner image).
	Image func(ctx context.Context) (string, error)
	// Credentials prepares an agent's credentials, valid for at least the
	// given duration; nil reads the host's (HostCredentials of $HOME).
	Credentials func(agent string, minValid time.Duration) (Credential, error)
	// LookPath finds the agent binary on the host; nil is exec.LookPath.
	LookPath func(command string) (string, error)
	// AimanBin is mounted read-only into the proxy container.
	AimanBin string
	UID, GID int
	Now      func() time.Time
}

// Run runs j's agent headless with only j.Dir mounted (read-only, at /job)
// beside the agent binary and its credential copy, the brief on stdin and
// egress only to the agent's provider. The env file and credential copy go
// to a temporary directory outside j.Dir, removed when Run returns.
func Run(ctx context.Context, o Options, j Job) (JobResult, error) {
	res := JobResult{Agent: j.Agent, Model: j.Model}
	if !Supported(j.Agent) {
		return res, fmt.Errorf("agent job: the agent container cannot run %q", j.Agent)
	}
	if j.Name == "" || j.Dir == "" {
		return res, errors.New("agent job: a name and a job directory are required")
	}
	if o.Image == nil || o.AimanBin == "" {
		return res, errors.New("agent job: Image and AimanBin are required")
	}
	dir, err := filepath.Abs(j.Dir)
	if err != nil {
		return res, fmt.Errorf("agent job: %w", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return res, fmt.Errorf("agent job: %s is not a directory", dir)
	}
	runner, now, lookPath, creds := o.Runner, o.Now, o.LookPath, o.Credentials
	if runner == nil {
		runner = DockerRunner{}
	}
	if now == nil {
		now = time.Now
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if creds == nil {
		home, err := os.UserHomeDir()
		if err != nil {
			return res, fmt.Errorf("agent job: %w", err)
		}
		creds = HostCredentials(home)
	}
	timeout := j.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	bin, err := lookPath(j.Agent)
	if err == nil {
		bin, err = filepath.EvalSymlinks(bin)
	}
	if err == nil && !IsELF(bin) {
		err = errors.New("not a native binary")
	}
	if err != nil {
		return res, fmt.Errorf("agent job: %s: %w", j.Agent, err)
	}
	cred, err := creds(j.Agent, timeout+5*time.Minute)
	if err != nil {
		return res, fmt.Errorf("agent job: %s credentials: %w", j.Agent, err)
	}
	image, err := o.Image(ctx)
	if err != nil {
		return res, fmt.Errorf("agent job: container image: %w", err)
	}
	work, err := os.MkdirTemp("", "aiman-agentjob-")
	if err != nil {
		return res, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	c := Container{
		Name: j.Name, Image: image, UID: o.UID, GID: o.GID, AimanBin: o.AimanBin,
		Workdir: JobDir, Stdin: j.Brief, Timeout: timeout,
		Mounts: []Mount{{Host: dir, Container: JobDir, ReadOnly: true}},
	}
	if err := Prepare(work, &c, Launch{Agent: j.Agent, Model: j.Model, Bin: bin, Cred: cred}); err != nil {
		return res, fmt.Errorf("agent job: %w", err)
	}
	start := now()
	out, err := runner.Run(ctx, c)
	res.Stdout, res.Duration = out, now().Sub(start)
	if err != nil {
		return res, fmt.Errorf("agent job %s: %w", j.Name, err)
	}
	return res, nil
}
