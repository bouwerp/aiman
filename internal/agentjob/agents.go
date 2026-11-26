package agentjob

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// agentSpec is how one agent CLI runs headless in the container.
type agentSpec struct {
	// credTarget is where the credential copy goes under HOME.
	credTarget string
	// hosts are the provider API endpoints for a credential mode.
	hosts func(mode string) []string
	// mount answers the host path to mount read-only, its container path,
	// and the binary to run inside.
	mount func(bin string) (host, container, exe string)
	args  func(exe, model, effort, workdir string, cred Credential) []string
	env   []string
}

// agents are the agents the container can run: native binaries with a
// headless print mode and a known provider endpoint.
var agents = map[string]agentSpec{
	"claude": { //nolint:gosec // G101: an agent name and paths, no credential
		credTarget: ".claude/.credentials.json",
		hosts:      func(string) []string { return []string{"api.anthropic.com:443"} },
		mount: func(bin string) (string, string, string) {
			return bin, agentDir + "/claude", agentDir + "/claude"
		},
		args: func(exe, model, effort, _ string, cred Credential) []string {
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
				return filepath.Dir(filepath.Dir(bin)), agentDir, agentDir + "/bin/" + filepath.Base(bin)
			}
			return bin, agentDir + "/codex", agentDir + "/codex"
		},
		args: func(exe, model, effort, workdir string, _ Credential) []string {
			// Codex's own sandbox needs user namespaces the container does not
			// grant; the container is the sandbox (read-only root and mounts,
			// tmpfs home, egress only to the provider).
			a := []string{exe, "exec", "--ephemeral", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules",
				"--sandbox", "danger-full-access", "--color", "never", "-C", workdir, "-c", `web_search="disabled"`}
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

// Agents lists the agents the container can run, sorted.
func Agents() []string {
	out := make([]string, 0, len(agents))
	for k := range agents {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Supported reports whether the container can run agent.
func Supported(agent string) bool {
	_, ok := agents[agent]
	return ok
}

func specFor(agent string) (agentSpec, error) {
	spec, ok := agents[agent]
	if !ok {
		return agentSpec{}, fmt.Errorf("the agent container cannot run %s", agent)
	}
	return spec, nil
}

// Entrypoint is agent's headless argv for a job (binary at its default
// container path, working in /job) and the proxy allowlist for cred.
func Entrypoint(agent, model string, cred Credential) ([]string, []string, error) {
	spec, err := specFor(agent)
	if err != nil {
		return nil, nil, err
	}
	_, _, exe := spec.mount(agent)
	return spec.args(exe, model, "", JobDir, cred), spec.hosts(cred.Mode), nil
}

// Egress is the proxy allowlist of agent with cred: its provider's API
// endpoints. It is empty for an agent the container cannot run.
func Egress(agent string, cred Credential) []string {
	spec, ok := agents[agent]
	if !ok {
		return nil
	}
	return spec.hosts(cred.Mode)
}

// Launch is the agent Prepare puts into a container.
type Launch struct {
	Agent, Model, Effort string
	// Bin is the agent's binary on the host (symlinks resolved).
	Bin  string
	Cred Credential
}

// Prepare fills c for l: the agent binary's read-only mount, the
// credential copy's mount, the 0600 env file and the credential copy
// written under work (never a directory the agent reads), the proxy
// allowlist and the entrypoint. c.Workdir is where the agent works.
func Prepare(work string, c *Container, l Launch) error {
	spec, err := specFor(l.Agent)
	if err != nil {
		return err
	}
	host, inside, exe := spec.mount(l.Bin)
	c.Allow = spec.hosts(l.Cred.Mode)
	c.Mounts = append(c.Mounts, Mount{Host: host, Container: inside, ReadOnly: true})
	proxy := fmt.Sprintf("http://proxy:%d", ProxyPort)
	env := []string{"HOME=" + home, "TMPDIR=/tmp", "HTTPS_PROXY=" + proxy, "https_proxy=" + proxy,
		"HTTP_PROXY=" + proxy, "http_proxy=" + proxy, "ALL_PROXY=" + proxy, "NO_PROXY=", "no_proxy=",
		"AIMAN_CRED_TARGET=" + home + "/" + spec.credTarget}
	env = append(env, spec.env...)
	for _, k := range sortedKeys(l.Cred.Env) {
		env = append(env, k+"="+l.Cred.Env[k])
	}
	for _, e := range env {
		if strings.ContainsAny(e, "\r\n\x00") {
			return errors.New("a review environment value holds a line break")
		}
	}
	c.EnvFile = filepath.Join(work, "env")
	if err := os.WriteFile(c.EnvFile, []byte(strings.Join(env, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if l.Cred.File != nil {
		cred := filepath.Join(work, "credential")
		if err := os.WriteFile(cred, l.Cred.File, 0o600); err != nil {
			return err
		}
		c.Mounts = append(c.Mounts, Mount{Host: cred, Container: credIn, ReadOnly: true})
	}
	// The read-only credential is copied into the tmpfs HOME, where the
	// agent expects it; the copy holds no refresh token.
	script := `set -e
if [ -f ` + credIn + ` ]; then
  mkdir -p "$(dirname "$AIMAN_CRED_TARGET")"
  cp ` + credIn + ` "$AIMAN_CRED_TARGET"
  chmod 600 "$AIMAN_CRED_TARGET"
fi
exec "$@"`
	c.Entrypoint = append([]string{"/bin/sh", "-c", script, "sh"}, spec.args(exe, l.Model, l.Effort, c.Workdir, l.Cred)...)
	return nil
}

// IsELF reports whether p is a native ELF binary, the only kind the
// container can run.
func IsELF(p string) bool {
	f, err := os.Open(p) //nolint:gosec // G304: an agent binary LookPath found
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	var magic [4]byte
	_, err = f.Read(magic[:])
	return err == nil && string(magic[:]) == "\x7fELF"
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
