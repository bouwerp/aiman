package harness

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
)

// DebugAdapter is one language's debugger. The compose builder, the edge's
// exclusive DAP relay, the console's DAP bridge and its Debug tab know only
// this interface and the Debug Adapter Protocol; Go (Delve) is the one
// implementation today.
type DebugAdapter interface {
	// Lang is the app.debug.lang it serves.
	Lang() string
	// Spec readies the adapter's tools on the host and says how the app
	// container runs under it.
	Spec(ctx context.Context) (*DebugSpec, error)
	// Launch is the DAP launch request a client sends for the file's app.
	Launch(f *File) (DebugLaunch, error)
	// EditorConfig is a .vscode/launch.json configuration that connects an
	// editor to the debugger forwarded to 127.0.0.1:port.
	EditorConfig(l DebugLaunch, port int) map[string]any
}

// DebugLaunch is what a DAP client needs to start the app: the launch
// request's arguments, sent as they are, and the container path of the run's
// exported tree for mapping source paths.
type DebugLaunch struct {
	Lang       string         `json:"lang"`
	Adapter    string         `json:"adapter"`
	Arguments  map[string]any `json:"arguments"`
	SourceRoot string         `json:"source_root"`
}

// CodeDebugUnsupported is a debug run for a language with no adapter yet.
const CodeDebugUnsupported = "harness_debug_unsupported"

// DebugAdapterFor picks the adapter for app.debug.lang among adapters.
func DebugAdapterFor(f *File, adapters []DebugAdapter) (DebugAdapter, error) {
	if f.App.Debug == nil {
		return nil, invalid("a debug run needs app.debug in the harness file (for example debug: {lang: go})")
	}
	lang := f.App.Debug.Lang
	for _, a := range adapters {
		if a.Lang() == lang {
			return a, nil
		}
	}
	return nil, &Error{Code: CodeDebugUnsupported, Msg: fmt.Sprintf("debugging app.debug.lang %q is not yet supported; only go is", lang)}
}

// EditorConfig is the launch.json configuration for l at 127.0.0.1:port,
// from the adapter of l's language.
func EditorConfig(l DebugLaunch, port int) (map[string]any, error) {
	for _, a := range []DebugAdapter{&GoDebug{}} {
		if a.Lang() == l.Lang {
			return a.EditorConfig(l, port), nil
		}
	}
	return nil, fmt.Errorf("no editor configuration for %q", l.Lang)
}

// GoDebug is the Go adapter: `dlv dap` from the pinned, host-built Delve.
type GoDebug struct {
	Tools *DlvTools
}

// DlvMount is where the pinned dlv is mounted in the app container.
const DlvMount = ToolsMount + "/dlv"

// goDAPPort is dlv dap's port on the app's debug address.
const goDAPPort = 2345

// GoDebugFlags are the go build flags of a debug run: no optimisation and no
// inlining, so every line and variable is where the source says.
const GoDebugFlags = "'-gcflags=all=-N -l'"

// Lang is go.
func (*GoDebug) Lang() string { return "go" }

// Spec builds or verifies dlv and runs it on the debug alias.
func (g *GoDebug) Spec(ctx context.Context) (*DebugSpec, error) {
	if g.Tools == nil {
		return nil, errors.New("the Go debugger has no tools directory")
	}
	dlv, err := g.Tools.Ensure(ctx)
	if err != nil {
		return nil, err
	}
	return GoDebugSpec(dlv), nil
}

// GoDebugSpec runs `dlv dap` on $AIMAN_INSPECT, the app's debug address. It serves one client and exits
// when that client leaves, so a loop starts the next one; a take over
// therefore restarts the debugger and the app for the new client. No
// --accept-multiclient and no --api-version: those belong to Delve's JSON-RPC
// API. GOFLAGS turns optimisation off for app.build.
func GoDebugSpec(dlv string) *DebugSpec {
	script := "trap 'kill $p 2>/dev/null; exit 0' TERM INT\n" +
		"while :; do\n" +
		"  " + DlvMount + " dap --listen=\"$" + InspectEnv + "\" --check-go-version=false & p=$!\n" +
		"  wait $p\n" +
		"  sleep 1\n" +
		"done"
	return &DebugSpec{
		Lang:    "go",
		Command: []string{"sh", "-c", script},
		Env:     map[string]string{"GOFLAGS": GoDebugFlags},
		Mounts:  []ToolMount{{Host: dlv, Container: DlvMount}},
		Port:    goDAPPort,
	}
}

// Launch is mode exec of the built binary: app.debug.program and args when
// set, else app.run split into words.
func (*GoDebug) Launch(f *File) (DebugLaunch, error) {
	prog, args, err := debugProgram(f)
	if err != nil {
		return DebugLaunch{}, err
	}
	return DebugLaunch{
		Lang: "go", Adapter: "dlv dap", SourceRoot: "/src",
		Arguments: map[string]any{
			"name": "aiman harness", "type": "go", "request": "launch", "mode": "exec",
			"program": prog, "args": args, "cwd": "/src", "stopOnEntry": false,
		},
	}, nil
}

// EditorConfig is VS Code Go's launch request against an external dlv dap
// server at 127.0.0.1:port; the workspace maps to /src. It is a launch, not
// review.LaunchConfig's attach in remote mode, because dlv dap starts the
// program itself.
func (*GoDebug) EditorConfig(l DebugLaunch, port int) map[string]any {
	cfg := map[string]any{
		"name": "aiman harness (Go)", "type": "go", "request": "launch", "mode": "exec",
		"host": "127.0.0.1", "port": port,
		"substitutePath": []map[string]string{{"from": "${workspaceFolder}", "to": l.SourceRoot}},
	}
	for _, k := range []string{"program", "args", "cwd"} {
		if v, ok := l.Arguments[k]; ok {
			cfg[k] = v
		}
	}
	return cfg
}

// debugProgram is the program and arguments a debug run starts.
func debugProgram(f *File) (string, []string, error) {
	d := f.App.Debug
	if d != nil && d.Program != "" {
		return d.Program, append([]string{}, d.Args...), nil
	}
	words, err := splitCommand(f.App.Run)
	if err != nil || len(words) == 0 {
		return "", nil, invalid(fmt.Sprintf("app.run %q cannot be started under the debugger (%v); set app.debug.program and app.debug.args", f.App.Run, err))
	}
	if words[0] == "exec" {
		words = words[1:]
	}
	if len(words) == 0 {
		return "", nil, invalid("app.run names no program; set app.debug.program")
	}
	prog := words[0]
	if !path.IsAbs(prog) {
		prog = path.Join("/src", prog)
	}
	return prog, words[1:], nil
}

// splitCommand splits a plain command line into words with single and double
// quotes. Anything that needs a shell (expansion, pipes, redirects, lists,
// globs, variable assignments) is refused rather than guessed at.
func splitCommand(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	quote := rune(0)
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			if quote == '"' && strings.ContainsRune("$`\\", r) {
				return nil, fmt.Errorf("%q inside double quotes needs a shell", r)
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case strings.ContainsRune("$`\\;&|<>(){}*?[]~", r), r == '#' && !inWord:
			return nil, fmt.Errorf("%q needs a shell", r)
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	if inWord {
		words = append(words, cur.String())
	}
	if len(words) > 0 && strings.Contains(strings.SplitN(words[0], "/", 2)[0], "=") {
		return nil, errors.New("a variable assignment needs a shell")
	}
	return words, nil
}
