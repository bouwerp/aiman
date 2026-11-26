// Package agentjob runs one headless agent call in a hardened container on
// an internal network whose only other member is an allowlist proxy that
// reaches the agent's provider and nothing else. The image review and the
// event feed both use it.
package agentjob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Container limits, paths and the proxy's contract. ProxyPort,
// ProxyReadyLine and ContainerAiman match the review proxy in
// internal/harness/images (aiman harness review-proxy).
const (
	Memory         = "2g"
	CPUs           = "2"
	PIDs           = 256
	ProxyPort      = 3128
	ProxyReadyLine = "aiman review proxy listening on "
	ContainerAiman = "/aiman"
	// DefaultTimeout bounds one agent run when the container names none.
	DefaultTimeout = 10 * time.Minute
	// Label marks every container and network this package creates.
	Label       = "aiman.review=1"
	home        = "/home/review"
	agentDir    = "/opt/agent"
	credIn      = "/run/aiman/credential" //nolint:gosec // G101: a container path, not a credential
	proxyMemory = "128m"
	proxyPIDs   = 64
	outMax      = 1 << 20
)

// Mount is one bind mount. The agent container mounts everything read-only.
type Mount struct {
	Host, Container string
	ReadOnly        bool
}

// Container is one agent run: the agent container on an internal network
// whose only other member is the allowlist proxy.
type Container struct {
	// Name names the agent container and the network; the proxy is
	// <Name>-proxy.
	Name     string
	Image    string
	UID, GID int
	// Mounts are all read-only.
	Mounts []Mount
	// EnvFile holds HOME, the proxy variables and any API key (0600).
	EnvFile    string
	Entrypoint []string
	// Workdir is the agent's working directory; empty keeps the image's.
	Workdir string
	// Stdin is the brief.
	Stdin string
	// Allow is the proxy's allowlist.
	Allow    []string
	AimanBin string
	Timeout  time.Duration
}

// Runner runs an agent container and returns the agent's stdout.
type Runner interface {
	Run(ctx context.Context, c Container) (string, error)
}

// DockerRun runs docker with args and stdin and returns stdout and stderr
// apart; err is set only when docker could not run.
type DockerRun func(ctx context.Context, args []string, stdin string) (stdout, stderr string, code int, err error)

// ExecDockerRun runs the docker CLI, keeping at most 1 MiB of stdout.
func ExecDockerRun(ctx context.Context, args []string, stdin string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // G204: aiman's own docker arguments
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = strings.NewReader(stdin)
	out := &capped{max: outMax}
	var errb capped
	errb.max = 64 << 10
	cmd.Stdout, cmd.Stderr = out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.String(), errb.String(), 0, nil
	case ctx.Err() != nil:
		return out.String(), errb.String(), -1, ctx.Err()
	case errors.As(err, &ee) && ee.ExitCode() > 0:
		return out.String(), errb.String(), ee.ExitCode(), nil
	default:
		return out.String(), errb.String(), -1, err
	}
}

// capped keeps the first max bytes written and drops the rest.
type capped struct {
	bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.Len(); room > 0 {
		c.Buffer.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// DockerRunner runs the container on the host's Docker engine.
type DockerRunner struct {
	// Docker is nil for ExecDockerRun.
	Docker DockerRun
}

func (r DockerRunner) docker() DockerRun {
	if r.Docker != nil {
		return r.Docker
	}
	return ExecDockerRun
}

// hardening are the flags the agent and proxy containers share.
func hardening(uid, gid int, memory, cpus string, pids int) []string {
	return []string{"--read-only", "--user", fmt.Sprintf("%d:%d", uid, gid),
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--memory", memory, "--memory-swap", memory, "--cpus", cpus, "--pids-limit", strconv.Itoa(pids)}
}

// Args are the docker run arguments of the agent container.
func Args(c Container) []string {
	args := []string{"run", "--rm", "-i", "--name", c.Name, "--network", c.Name, "--label", Label}
	args = append(args, hardening(c.UID, c.GID, Memory, CPUs, PIDs)...)
	args = append(args, "--tmpfs", "/tmp:rw,nosuid,nodev,size=256m",
		"--tmpfs", fmt.Sprintf("%s:rw,nosuid,nodev,size=256m,uid=%d,gid=%d,mode=0700", home, c.UID, c.GID))
	for _, m := range c.Mounts {
		args = append(args, "--mount", "type=bind,source="+m.Host+",target="+m.Container+",readonly")
	}
	if c.EnvFile != "" {
		args = append(args, "--env-file", c.EnvFile)
	}
	if c.Workdir != "" {
		args = append(args, "--workdir", c.Workdir)
	}
	args = append(args, "--entrypoint", c.Entrypoint[0], c.Image)
	return append(args, c.Entrypoint[1:]...)
}

// ProxyArgs are the docker run arguments of the allowlist proxy: the aiman
// binary read-only, on the internal network only until it listens.
func ProxyArgs(c Container) []string {
	args := []string{"run", "-d", "--name", c.Name + "-proxy", "--network", c.Name, "--network-alias", "proxy", "--label", Label}
	args = append(args, hardening(c.UID, c.GID, proxyMemory, "0.5", proxyPIDs)...)
	args = append(args, "--mount", "type=bind,source="+c.AimanBin+",target="+ContainerAiman+",readonly",
		"--entrypoint", ContainerAiman, c.Image, "harness", "review-proxy", "--listen", fmt.Sprintf("auto:%d", ProxyPort))
	for _, a := range c.Allow {
		args = append(args, "--allow", a)
	}
	return args
}

// Run creates an internal network, starts the proxy on it, connects the
// proxy (only) to the default bridge once it listens on the internal
// address, runs the agent container with the brief on stdin, and removes
// all three whatever happens.
func (r DockerRunner) Run(ctx context.Context, c Container) (string, error) {
	if len(c.Entrypoint) == 0 || c.AimanBin == "" || len(c.Allow) == 0 {
		return "", errors.New("review container: entrypoint, aiman binary and allowlist are required")
	}
	d := r.docker()
	proxy := c.Name + "-proxy"
	cleanup := func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _, _, _ = d(cctx, []string{"rm", "-f", c.Name, proxy}, "")
		_, _, _, _ = d(cctx, []string{"network", "rm", c.Name}, "")
	}
	cleanup()
	defer cleanup()
	if err := r.must(ctx, "create the review network", "network", "create", "--internal", "--label", Label, c.Name); err != nil {
		return "", err
	}
	if err := r.must(ctx, "start the review proxy", ProxyArgs(c)...); err != nil {
		return "", err
	}
	if err := r.waitProxy(ctx, proxy); err != nil {
		return "", err
	}
	if err := r.must(ctx, "connect the review proxy", "network", "connect", "bridge", proxy); err != nil {
		return "", err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, errOut, code, err := d(rctx, Args(c), c.Stdin)
	switch {
	case rctx.Err() != nil:
		return out, fmt.Errorf("the review ran out of time (%s)", timeout)
	case err != nil:
		return out, fmt.Errorf("run the review container: %w", err)
	case code != 0:
		return out, fmt.Errorf("the review agent exited %d: %s", code, printable(truncate(lastLines(errOut), 400)))
	}
	return out, nil
}

func (r DockerRunner) must(ctx context.Context, what string, args ...string) error {
	out, errOut, code, err := r.docker()(ctx, args, "")
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if code != 0 {
		return fmt.Errorf("%s: docker exited %d: %s", what, code, lastLines(errOut+out))
	}
	return nil
}

func (r DockerRunner) waitProxy(ctx context.Context, name string) error {
	deadline := time.Now().Add(20 * time.Second)
	for {
		out, errOut, _, err := r.docker()(ctx, []string{"logs", name}, "")
		if err == nil && strings.Contains(out+errOut, ProxyReadyLine) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the review proxy did not start: %s", lastLines(errOut+out))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func lastLines(s string) string {
	const n = 5
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// printable keeps letters, marks, numbers, punctuation, symbols and spaces
// on one line; anything else becomes \xNN or \uNNNN.
func printable(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n <= 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == ' ' || (unicode.IsPrint(r) && !unicode.Is(unicode.Cf, r)):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
		i += n
	}
	return b.String()
}
