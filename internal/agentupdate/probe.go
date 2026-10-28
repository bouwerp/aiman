package agentupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Executor runs one command and returns its combined output.
type Executor interface {
	Run(ctx context.Context, c Command) (string, error)
}

// Fetcher performs an HTTP GET. Latest-version probes that use the network go
// through it so tests never reach the network.
type Fetcher interface {
	Get(ctx context.Context, url string) ([]byte, error)
}

// LocalExec runs commands on this host with the process environment plus
// Command.Env.
type LocalExec struct{}

// Run implements Executor.
func (LocalExec) Run(ctx context.Context, c Command) (string, error) {
	if len(c.Argv) == 0 {
		return "", fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...) //nolint:gosec // G204: argv comes from the fixed agent catalog
	cmd.Env = mergeEnv(os.Environ(), c.Env)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mergeEnv(base []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return base
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(base)+len(keys))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, override := extra[k]; !override {
			out = append(out, kv)
		}
	}
	for _, k := range keys {
		out = append(out, k+"="+extra[k])
	}
	return out
}

// HTTPFetcher is the network Fetcher. Bodies are capped at 1 MiB.
type HTTPFetcher struct {
	Client *http.Client
}

// Get implements Fetcher.
func (f HTTPFetcher) Get(ctx context.Context, url string) ([]byte, error) {
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// ProbeTimeout bounds each probe command or request.
const ProbeTimeout = 20 * time.Second

// Prober reads installed and latest versions. It never runs an update
// subcommand: the only agent invocation is `<agent> --version`, and every
// probe command for an agent carries that agent's guard env.
type Prober struct {
	Exec Executor
	HTTP Fetcher
	Now  func() time.Time
}

func (p *Prober) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// ResolveCommand resolves the agent on PATH to its real file.
func ResolveCommand(e Entry) Command {
	return Command{
		Argv: []string{"sh", "-c", fmt.Sprintf(`p=$(command -v %s) && readlink -f "$p"`, e.Agent)},
		Env:  copyEnv(e.GuardEnv),
	}
}

func homeFileCommand(e Entry, rel string) Command {
	return Command{Argv: []string{"sh", "-c", fmt.Sprintf(`cat "$HOME/%s"`, rel)}, Env: copyEnv(e.GuardEnv)}
}

// InstalledCommand is the command that prints the installed version.
func InstalledCommand(e Entry) Command {
	if e.VersionFile != "" {
		return homeFileCommand(e, e.VersionFile)
	}
	return Command{Argv: []string{e.Agent, "--version"}, Env: copyEnv(e.GuardEnv)}
}

// Probe reads one agent. installed is false when the agent is not on PATH.
func (p *Prober) Probe(ctx context.Context, e Entry) (state AgentState, installed bool) {
	path, err := p.run(ctx, ResolveCommand(e))
	path = strings.TrimSpace(path)
	if err != nil || path == "" {
		return AgentState{}, false
	}
	state = AgentState{
		Agent:     e.Agent,
		Path:      path,
		Method:    e.DetectMethod(path),
		CheckedAt: p.now().UTC(),
	}
	var errs []string
	if out, err := p.run(ctx, InstalledCommand(e)); err != nil {
		errs = append(errs, fmt.Sprintf("version: %v: %s", err, firstLine(out)))
	} else if v, err := e.ParseVersion(out); err != nil {
		errs = append(errs, "version: "+err.Error())
	} else {
		state.Installed = v
	}
	if latest, err := p.latest(ctx, e); err != nil {
		errs = append(errs, "latest: "+err.Error())
	} else {
		state.Latest = latest
	}
	state.Error = strings.Join(errs, "; ")
	return state, true
}

// ProbeAll probes every entry and returns the installed ones keyed by agent.
func (p *Prober) ProbeAll(ctx context.Context, entries []Entry) map[string]AgentState {
	out := make(map[string]AgentState)
	for _, e := range entries {
		if s, ok := p.Probe(ctx, e); ok {
			out[e.Agent] = s
		}
	}
	return out
}

func (p *Prober) run(ctx context.Context, c Command) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	return p.Exec.Run(ctx, c)
}

func (p *Prober) fetch(ctx context.Context, url string) ([]byte, error) {
	if p.HTTP == nil {
		return nil, fmt.Errorf("no HTTP fetcher")
	}
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	return p.HTTP.Get(ctx, url)
}

func (p *Prober) latest(ctx context.Context, e Entry) (string, error) {
	src := e.Latest
	switch src.Kind {
	case LatestHTTPText:
		b, err := p.fetch(ctx, src.URL)
		if err != nil {
			return "", err
		}
		return e.ParseVersion(string(b))
	case LatestHTTPJSON:
		b, err := p.fetch(ctx, src.URL)
		if err != nil {
			return "", err
		}
		return jsonVersion(e, b, src.Fields)
	case LatestFileJSON:
		out, err := p.run(ctx, homeFileCommand(e, src.File))
		if err != nil {
			return "", fmt.Errorf("%s: %v", src.File, err)
		}
		return jsonVersion(e, []byte(out), src.Fields)
	case LatestNPM:
		out, err := p.run(ctx, Command{Argv: []string{"npm", "view", src.Package, "version"}})
		if err != nil {
			return "", fmt.Errorf("npm view %s: %v", src.Package, err)
		}
		return e.ParseVersion(out)
	default:
		return "", nil
	}
}

func jsonVersion(e Entry, b []byte, fields []string) (string, error) {
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		return "", err
	}
	for _, f := range fields {
		if s, ok := obj[f].(string); ok && s != "" {
			return e.ParseVersion(s)
		}
	}
	return "", fmt.Errorf("%w in fields %v", errNoVersion, fields)
}
