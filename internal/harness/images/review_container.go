package images

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Review container limits and paths.
const (
	ReviewMemory   = "2g"
	ReviewCPUs     = "2"
	ReviewPIDs     = 256
	reviewHome     = "/home/review"
	reviewBundle   = "/bundle"
	reviewAgentDir = "/opt/agent"
	reviewCredIn   = "/run/aiman/credential" //nolint:gosec // G101: a container path, not a credential
	proxyMemory    = "128m"
	proxyPIDs      = 64
	reviewOutMax   = 1 << 20
)

// reviewAgent is how one agent CLI runs headless in the review container.
type reviewAgent struct {
	// credTarget is where the credential copy goes under HOME.
	credTarget string
	// hosts are the provider API endpoints for a credential mode.
	hosts func(mode string) []string
	// mount answers the host path to mount read-only, its container path,
	// and the binary to run inside.
	mount func(bin string) (host, container, exe string)
	args  func(exe, model, effort string, cred Credential) []string
	env   []string
}

// reviewAgents are the agents the review container can run: native
// binaries with a headless print mode and a known provider endpoint.
var reviewAgents = map[string]reviewAgent{
	"claude": { //nolint:gosec // G101: an agent name and paths, no credential
		credTarget: ".claude/.credentials.json",
		hosts:      func(string) []string { return []string{"api.anthropic.com:443"} },
		mount: func(bin string) (string, string, string) {
			return bin, reviewAgentDir + "/claude", reviewAgentDir + "/claude"
		},
		args: func(exe, model, effort string, cred Credential) []string {
			// Read, Grep and Glob are the only tools; dontAsk denies anything
			// that would prompt; no MCP servers, no session files.
			a := []string{exe, "-p", "--output-format", "text", "--tools", "Read,Grep,Glob",
				"--permission-mode", "dontAsk", "--strict-mcp-config", "--no-session-persistence"}
			if cred.Mode == CredentialAPIKey {
				a = append(a, "--bare")
			}
			if model != "" {
				a = append(a, "--model", model)
			}
			if effort != "" {
				a = append(a, "--effort", effort)
			}
			return a
		},
		env: []string{"DISABLE_AUTOUPDATER=1", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"},
	},
	"codex": { //nolint:gosec // G101: an agent name and paths, no credential
		credTarget: ".codex/auth.json",
		hosts: func(mode string) []string {
			if mode == CredentialAPIKey {
				return []string{"api.openai.com:443"}
			}
			return []string{"chatgpt.com:443"}
		},
		mount: func(bin string) (string, string, string) {
			// The standalone release keeps helpers beside bin/.
			if filepath.Base(filepath.Dir(bin)) == "bin" {
				return filepath.Dir(filepath.Dir(bin)), reviewAgentDir, reviewAgentDir + "/bin/" + filepath.Base(bin)
			}
			return bin, reviewAgentDir + "/codex", reviewAgentDir + "/codex"
		},
		args: func(exe, model, effort string, _ Credential) []string {
			// Codex's own sandbox needs user namespaces the container does not
			// grant; the container is the sandbox (read-only root and bundle,
			// tmpfs home, egress only to the provider).
			a := []string{exe, "exec", "--ephemeral", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules",
				"--sandbox", "danger-full-access", "--color", "never", "-C", reviewBundle, "-c", `web_search="disabled"`}
			if model != "" {
				a = append(a, "-m", model)
			}
			if effort != "" {
				a = append(a, "-c", "model_reasoning_effort="+strconv.Quote(effort))
			}
			return append(a, "-")
		},
	},
}

// ReviewAgents lists the agents the review container can run.
func ReviewAgents() []string {
	out := make([]string, 0, len(reviewAgents))
	for k := range reviewAgents {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// ReviewContainer is one review run: the agent container on an internal
// network whose only other member is the allowlist proxy.
type ReviewContainer struct {
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
	// Stdin is the brief.
	Stdin string
	// Allow is the proxy's allowlist.
	Allow    []string
	AimanBin string
	Timeout  time.Duration
}

// ReviewRunner runs a review container and returns the agent's stdout.
type ReviewRunner interface {
	Run(ctx context.Context, c ReviewContainer) (string, error)
}

// container prepares the run: credential copy, env file and entrypoint.
func (m *ModelReviewer) container(work, image string, rec Record, p pick, info BundleInfo) (ReviewContainer, error) {
	host, inside, exe := p.spec.mount(p.bin)
	c := ReviewContainer{
		Name: "aiman-review-" + ShortDigest(rec.Digest), Image: image, UID: m.o.UID, GID: m.o.GID,
		Allow: p.spec.hosts(p.cred.Mode), AimanBin: m.o.AimanBin,
		Mounts: []Mount{
			{Host: info.Dir, Container: reviewBundle, ReadOnly: true},
			{Host: host, Container: inside, ReadOnly: true},
		},
	}
	proxy := fmt.Sprintf("http://proxy:%d", ProxyPort)
	env := []string{"HOME=" + reviewHome, "TMPDIR=/tmp", "HTTPS_PROXY=" + proxy, "https_proxy=" + proxy,
		"HTTP_PROXY=" + proxy, "http_proxy=" + proxy, "ALL_PROXY=" + proxy, "NO_PROXY=", "no_proxy=",
		"AIMAN_CRED_TARGET=" + reviewHome + "/" + p.spec.credTarget}
	env = append(env, p.spec.env...)
	for _, k := range sortedKeys(p.cred.Env) {
		env = append(env, k+"="+p.cred.Env[k])
	}
	for _, e := range env {
		if strings.ContainsAny(e, "\r\n\x00") {
			return c, errors.New("a review environment value holds a line break")
		}
	}
	c.EnvFile = filepath.Join(work, "env")
	if err := os.WriteFile(c.EnvFile, []byte(strings.Join(env, "\n")+"\n"), 0o600); err != nil {
		return c, err
	}
	if p.cred.File != nil {
		cred := filepath.Join(work, "credential")
		if err := os.WriteFile(cred, p.cred.File, 0o600); err != nil {
			return c, err
		}
		c.Mounts = append(c.Mounts, Mount{Host: cred, Container: reviewCredIn, ReadOnly: true})
	}
	// The read-only credential is copied into the tmpfs HOME, where the
	// agent expects it; the copy holds no refresh token.
	script := `set -e
if [ -f ` + reviewCredIn + ` ]; then
  mkdir -p "$(dirname "$AIMAN_CRED_TARGET")"
  cp ` + reviewCredIn + ` "$AIMAN_CRED_TARGET"
  chmod 600 "$AIMAN_CRED_TARGET"
fi
exec "$@"`
	c.Entrypoint = append([]string{"/bin/sh", "-c", script, "sh"}, p.spec.args(exe, p.entry.Model, p.entry.Effort, p.cred)...)
	return c, nil
}

// DockerRun runs docker with args and stdin and returns stdout and stderr
// apart; err is set only when docker could not run.
type DockerRun func(ctx context.Context, args []string, stdin string) (stdout, stderr string, code int, err error)

// ExecDockerRun runs the docker CLI, keeping at most 1 MiB of stdout.
func ExecDockerRun(ctx context.Context, args []string, stdin string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // G204: aiman's own docker arguments
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = strings.NewReader(stdin)
	out := &capped{max: reviewOutMax}
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

// DockerReviewRunner runs the review on the host's Docker engine.
type DockerReviewRunner struct {
	// Docker is nil for ExecDockerRun.
	Docker DockerRun
}

func (r DockerReviewRunner) docker() DockerRun {
	if r.Docker != nil {
		return r.Docker
	}
	return ExecDockerRun
}

// hardening are the flags both review containers share.
func hardening(uid, gid int, memory, cpus string, pids int) []string {
	return []string{"--read-only", "--user", fmt.Sprintf("%d:%d", uid, gid),
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--memory", memory, "--memory-swap", memory, "--cpus", cpus, "--pids-limit", strconv.Itoa(pids)}
}

// ReviewArgs are the docker run arguments of the agent container.
func ReviewArgs(c ReviewContainer) []string {
	args := []string{"run", "--rm", "-i", "--name", c.Name, "--network", c.Name, "--label", "aiman.review=1"}
	args = append(args, hardening(c.UID, c.GID, ReviewMemory, ReviewCPUs, ReviewPIDs)...)
	args = append(args, "--tmpfs", "/tmp:rw,nosuid,nodev,size=256m",
		"--tmpfs", fmt.Sprintf("%s:rw,nosuid,nodev,size=256m,uid=%d,gid=%d,mode=0700", reviewHome, c.UID, c.GID))
	for _, m := range c.Mounts {
		args = append(args, "--mount", "type=bind,source="+m.Host+",target="+m.Container+",readonly")
	}
	if c.EnvFile != "" {
		args = append(args, "--env-file", c.EnvFile)
	}
	args = append(args, "--workdir", reviewBundle, "--entrypoint", c.Entrypoint[0], c.Image)
	return append(args, c.Entrypoint[1:]...)
}

// ProxyArgs are the docker run arguments of the allowlist proxy: the aiman
// binary read-only, on the review network only until it listens.
func ProxyArgs(c ReviewContainer) []string {
	args := []string{"run", "-d", "--name", c.Name + "-proxy", "--network", c.Name, "--network-alias", "proxy", "--label", "aiman.review=1"}
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
func (r DockerReviewRunner) Run(ctx context.Context, c ReviewContainer) (string, error) {
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
	if err := r.must(ctx, "create the review network", "network", "create", "--internal", "--label", "aiman.review=1", c.Name); err != nil {
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
		timeout = DefaultReviewTimeout
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, errOut, code, err := d(rctx, ReviewArgs(c), c.Stdin)
	switch {
	case rctx.Err() != nil:
		return out, fmt.Errorf("the review ran out of time (%s)", timeout)
	case err != nil:
		return out, fmt.Errorf("run the review container: %w", err)
	case code != 0:
		return out, fmt.Errorf("the review agent exited %d: %s", code, printable(truncate(lastLines(errOut), 400), false))
	}
	return out, nil
}

func (r DockerReviewRunner) must(ctx context.Context, what string, args ...string) error {
	out, errOut, code, err := r.docker()(ctx, args, "")
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if code != 0 {
		return fmt.Errorf("%s: docker exited %d: %s", what, code, lastLines(errOut+out))
	}
	return nil
}

func (r DockerReviewRunner) waitProxy(ctx context.Context, name string) error {
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
