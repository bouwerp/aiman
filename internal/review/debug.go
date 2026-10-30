package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DebugPorts are the host-published ports of a review debug session.
type DebugPorts struct {
	Debug, App, DevTools int
}

const (
	launchJSONRel     = ".vscode/launch.json"
	goDAPPort         = 2345
	nodeInspectPort   = 9229
	reactAppPort      = 3000
	reactDevToolsPort = 8097
)

// LaunchConfig merges an attach configuration named "aiman: attach <lang>"
// into existing .vscode/launch.json. existing may be empty.
func LaunchConfig(lang string, ports DebugPorts, worktree string, existing []byte) ([]byte, error) {
	return LaunchConfigWithOrder(lang, ports, worktree, existing, nil)
}

// LaunchConfigWithOrder is LaunchConfig plus a description listing reading-order
// path:line entries. DAP clients do not keep Delve --init breakpoints.
func LaunchConfigWithOrder(lang string, ports DebugPorts, worktree string, existing []byte, order []string) ([]byte, error) {
	cfg, err := attachConfig(lang, ports, worktree, order)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	doc := launchDoc{Version: "0.2.0"}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &doc); err != nil {
			return nil, fmt.Errorf("launch.json: %w", err)
		}
		if doc.Version == "" {
			doc.Version = "0.2.0"
		}
		var kept []json.RawMessage
		for _, raw := range doc.Configurations {
			var n struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(raw, &n) == nil && n.Name == cfg.Name {
				continue
			}
			kept = append(kept, raw)
		}
		doc.Configurations = kept
	}
	doc.Configurations = append(doc.Configurations, body)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

type launchDoc struct {
	Version        string            `json:"version"`
	Configurations []json.RawMessage `json:"configurations"`
}

type pathMap struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type attachCfg struct {
	Name           string    `json:"name"`
	Type           string    `json:"type"`
	Request        string    `json:"request"`
	Mode           string    `json:"mode,omitempty"`
	Port           int       `json:"port"`
	Host           string    `json:"host,omitempty"`
	Address        string    `json:"address,omitempty"`
	SubstitutePath []pathMap `json:"substitutePath,omitempty"`
	LocalRoot      string    `json:"localRoot,omitempty"`
	RemoteRoot     string    `json:"remoteRoot,omitempty"`
	WebRoot        string    `json:"webRoot,omitempty"`
	URL            string    `json:"url,omitempty"`
	Description    string    `json:"description,omitempty"`
}

func attachConfig(lang string, ports DebugPorts, worktree string, order []string) (attachCfg, error) {
	lang = strings.ToLower(strings.TrimSpace(lang))
	name := "aiman: attach " + lang
	desc := strings.Join(order, ", ")
	switch lang {
	case "go":
		return attachCfg{
			Name: name, Type: "go", Request: "attach", Mode: "remote",
			Port: ports.Debug, Host: "127.0.0.1",
			SubstitutePath: []pathMap{{From: worktree, To: "/src"}},
			Description:    desc,
		}, nil
	case "node":
		return attachCfg{
			Name: name, Type: "node", Request: "attach",
			Port: ports.Debug, Address: "127.0.0.1",
			LocalRoot: worktree, RemoteRoot: "/src",
			Description: desc,
		}, nil
	case "react":
		return attachCfg{
			Name: name, Type: "chrome", Request: "attach",
			Port: ports.App, Host: "127.0.0.1",
			URL:     fmt.Sprintf("http://127.0.0.1:%d", ports.App),
			WebRoot: worktree, Description: desc,
		}, nil
	default:
		return attachCfg{}, fmt.Errorf("review debug: unknown lang %q", lang)
	}
}

// InitFromReadingOrder turns reading-order.txt (one path:line per line) into
// a Delve --init script: one `break` per line, in order. A missing or empty
// file is an empty script.
func InitFromReadingOrder(raw []byte) string {
	var b strings.Builder
	for _, line := range ReadingOrder(raw) {
		b.WriteString("break ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// ReadingOrder parses one path:line per line. Blank lines and # comments are skipped.
func ReadingOrder(raw []byte) []string {
	var out []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// PortMaps is the compose publish list for lang using host ports in p.
func PortMaps(lang string, p DebugPorts) []PortMap {
	debugC := nodeInspectPort
	if lang == "go" {
		debugC = goDAPPort
	}
	out := []PortMap{{Host: p.Debug, Container: debugC}}
	if lang == "react" {
		out = append(out,
			PortMap{Host: p.App, Container: reactAppPort},
			PortMap{Host: p.DevTools, Container: reactDevToolsPort},
		)
	}
	return out
}

// SandboxDebugCommand is the process that runs inside the sandbox for lang.
func SandboxDebugCommand(lang, target string) (string, error) {
	lang = strings.ToLower(strings.TrimSpace(lang))
	target = strings.TrimSpace(target)
	switch lang {
	case "go":
		return "export GOBIN=/cache/gocache/bin PATH=/cache/gocache/bin:$PATH\n" +
			"command -v dlv >/dev/null 2>&1 || go install github.com/go-delve/delve/cmd/dlv@latest\n" +
			fmt.Sprintf("exec dlv dap --listen=0.0.0.0:%d", goDAPPort), nil
	case "node":
		if target == "" {
			return "", errors.New("review debug: node needs --target")
		}
		return fmt.Sprintf("exec node --inspect=0.0.0.0:%d %s", nodeInspectPort, target), nil
	case "react":
		if target == "" {
			return "", errors.New("review debug: react needs --target")
		}
		return fmt.Sprintf(
			"PORT=%d npx react-devtools &\nNODE_OPTIONS=--inspect=0.0.0.0:%d PORT=%d HOST=0.0.0.0 exec %s",
			reactDevToolsPort, nodeInspectPort, reactAppPort, target,
		), nil
	default:
		return "", fmt.Errorf("review debug: unknown lang %q", lang)
	}
}

// WriteLaunchJSON writes .vscode/launch.json in worktree and lists it in
// git info/exclude so it is not staged with the PR.
func WriteLaunchJSON(worktree, lang string, ports DebugPorts, order []string) error {
	path := filepath.Join(worktree, launchJSONRel)
	existing, err := os.ReadFile(path) //nolint:gosec // worktree is the review PR checkout
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := LaunchConfigWithOrder(lang, ports, worktree, existing, order)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // .vscode is editor config
		return err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil { //nolint:gosec // launch.json is not secret
		return err
	}
	return ExcludeLaunchJSON(worktree)
}

// ExcludeLaunchJSON appends .vscode/launch.json to the repo's info/exclude
// when it is not already listed.
func ExcludeLaunchJSON(worktree string) error {
	cmd := exec.Command("git", "-C", worktree, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git exclude: %w", err)
	}
	path := strings.TrimSpace(string(out))
	body, err := os.ReadFile(path) //nolint:gosec // path is git's info/exclude
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == launchJSONRel {
			return nil
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // git info/exclude
	if err != nil {
		return err
	}
	defer f.Close()
	if len(body) > 0 && !bytes.HasSuffix(body, []byte("\n")) {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	_, err = f.WriteString(launchJSONRel + "\n")
	return err
}
