package harness

import (
	"context"
	"errors"
	"maps"
	"path"
	"runtime"
	"strconv"
	"strings"

	"github.com/bouwerp/aiman/internal/harness/tools"
	"github.com/bouwerp/aiman/internal/shellx"
)

// Node and TypeScript on Node. The app runs under the V8 inspector, bound to
// the app's debug address ($AIMAN_INSPECT) and waiting for a debugger before
// it runs (--inspect-wait). js-debug's standalone DAP server runs in the
// adapter container and attaches to that inspector over CDP (type pwa-node);
// the edge relays DAP to the adapter. js-debug opens the Node process as a
// child session through the startDebugging reverse request, so a client
// connects twice under one lease. TypeScript maps from tsx, ts-node,
// esbuild or tsc output (inline or beside the file, under /src or /out)
// resolve against the exported tree, which the adapter mounts read-only at
// the app's own path, so no path mapping is needed.
const (
	// NodeInspectPort is the inspector's port on the app's debug address.
	NodeInspectPort = 9229
	// JSDebugPort is js-debug's DAP port on the adapter's address.
	JSDebugPort = 8123
	// NodeToolMount and JSDebugMount are the tools' read-only mounts.
	NodeToolMount = ToolsMount + "/node"
	JSDebugMount  = ToolsMount + "/js-debug"
	// LocalRootKey in an editor's attach request names the editor's
	// workspace folder; the console's editor port maps it to /src.
	LocalRootKey = "aimanLocalRoot"
)

// AdapterImage is the adapter container's image: Debian 13 slim (glibc with
// libstdc++, which the Node build needs), pinned by its Docker Hub index
// digest and vetted as a builtin image like the edge's. It only gives the
// mounted tools a root filesystem.
const AdapterImage = "debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2"

// NodeDebug is the Node adapter.
type NodeDebug struct {
	// Tools keeps the pinned Node and js-debug on the host.
	Tools *tools.Store
	// Node and JSDebug override the pins (harness.debug_tools).
	Node, JSDebug tools.Pin
}

// Lang is node.
func (*NodeDebug) Lang() string { return "node" }

// Check accepts every parsed node block.
func (*NodeDebug) Check(f *File) error {
	if f.App.Debug == nil || f.App.Debug.Lang != "node" {
		return invalid("the node debugger needs app.debug with lang node")
	}
	_, err := nodeScript(f.App.Debug)
	return err
}

// AdapterImage is the pinned adapter container image.
func (*NodeDebug) AdapterImage() string { return AdapterImage }

// Spec fetches or verifies Node and js-debug and places them.
func (n *NodeDebug) Spec(ctx context.Context, f *File) (*DebugSpec, error) {
	if n.Tools == nil {
		return nil, errors.New("the Node debugger has no tools directory")
	}
	nodeSpec, err := tools.Node(runtime.GOARCH, n.Node)
	if err != nil {
		return nil, err
	}
	jsSpec, err := tools.JSDebug(n.JSDebug)
	if err != nil {
		return nil, err
	}
	nodeDir, err := n.Tools.Ensure(ctx, nodeSpec)
	if err != nil {
		return nil, toolError(err)
	}
	jsDir, err := n.Tools.Ensure(ctx, jsSpec)
	if err != nil {
		return nil, toolError(err)
	}
	return NodeDebugSpec(nodeDir, jsDir, f)
}

// CodeDebugTool refuses a debug run whose tool download failed its pinned
// checksum.
const CodeDebugTool = "harness_debug_tool"

func toolError(err error) error {
	var ce *tools.ChecksumError
	if errors.As(err, &ce) {
		return &Error{Code: CodeDebugTool, Tool: ce.Tool, Msg: "debug tool " + ce.Error() + "; nothing was installed"}
	}
	return err
}

// NodeDebugSpec runs the app under the inspector with the host's Node on
// PATH after the image's own, and js-debug in the adapter container.
func NodeDebugSpec(nodeDir, jsDebugDir string, f *File) (*DebugSpec, error) {
	if f.App.Debug == nil {
		return nil, invalid("the node debugger needs app.debug")
	}
	script, err := nodeScript(f.App.Debug)
	if err != nil {
		return nil, err
	}
	node := ToolMount{Host: nodeDir, Container: NodeToolMount}
	return &DebugSpec{
		Lang:    "node",
		Command: []string{"sh", "-c", script},
		Mounts:  []ToolMount{node},
		Port:    NodeInspectPort,
		Adapter: &AdapterSpec{
			Command: []string{"sh", "-c", "exec " + NodeToolMount + "/bin/node " + JSDebugMount + "/src/dapDebugServer.js " +
				strconv.Itoa(JSDebugPort) + ` "$` + AdapterHostEnv + `"`},
			Mounts: []ToolMount{node, {Host: jsDebugDir, Container: JSDebugMount}},
			Port:   JSDebugPort,
		},
	}, nil
}

// muslGuard stops a run whose image would start the mounted (glibc) runtime
// bin, named name, on a musl libc: it would fail with a loader error nobody
// reads.
func muslGuard(cmd, bin, name string) string {
	return `if [ "$(command -v ` + cmd + `)" = ` + bin + ` ] && ls /lib/ld-musl-* >/dev/null 2>&1; then
  echo "aiman: this image uses musl libc (Alpine) and has no ` + cmd + ` of its own; the mounted ` + name + ` needs glibc. Use an image with ` + cmd + ` installed, or a glibc image." >&2
  exit 1
fi
`
}

// nodeScript is the app command of a node debug run: the image's node
// first, the mounted one as a fallback, in /src or app.debug.cwd, either the
// file's own run or runtime --inspect-wait=$AIMAN_INSPECT runtime_args entry.
func nodeScript(d *Debug) (string, error) {
	return inspectScript(d, "node", NodeToolMount+"/bin", "Node")
}

// inspectScript is the app command of a node or bun debug run: the image's
// runtime first and the mounted one (in toolBin) as a fallback, refused on
// musl; then, in /src or app.debug.cwd, the file's own run or
// runtime --inspect-wait="$AIMAN_INSPECT" runtime_args entry.
func inspectScript(d *Debug, cmd, toolBin, name string) (string, error) {
	var b strings.Builder
	b.WriteString(`PATH="$PATH:` + toolBin + `"; export PATH` + "\n")
	b.WriteString(muslGuard(cmd, toolBin+"/"+cmd, name))
	b.WriteString("cd " + shellx.Quote(path.Join("/src", d.Cwd)) + " || exit 1\n")
	if d.Run != "" {
		b.WriteString(d.Run + "\n")
		return b.String(), nil
	}
	if d.Entry == "" {
		return "", invalid("app.debug: " + d.Lang + " needs entry or run")
	}
	rt := debugRuntime(d, cmd)
	words := []string{shellx.Quote(rt), `--inspect-wait="$` + InspectEnv + `"`}
	for _, a := range d.RuntimeArgs {
		words = append(words, shellx.Quote(a))
	}
	words = append(words, shellx.Quote(path.Join("/src", d.Entry)))
	b.WriteString("exec " + strings.Join(words, " ") + "\n")
	return b.String(), nil
}

// debugRuntime is the runtime a debug command starts: app.debug.runtime,
// else def; a repository path is under /src.
func debugRuntime(d *Debug, def string) string {
	rt := d.Runtime
	if rt == "" {
		rt = def
	}
	if strings.Contains(rt, "/") {
		rt = path.Join("/src", rt)
	}
	return rt
}

// Launch is js-debug's attach to the app's inspector. js-debug reads source
// maps from the adapter's /src and /out, the same paths the app sees.
func (*NodeDebug) Launch(f *File, net DebugNet, _ *DebugSpec) (DebugLaunch, error) {
	d := f.App.Debug
	if d == nil {
		return DebugLaunch{}, invalid("the node debugger needs app.debug")
	}
	return DebugLaunch{
		Lang: "node", Adapter: "js-debug", Request: "attach", SourceRoot: "/src", Children: true,
		Arguments: map[string]any{
			"name": "aiman harness", "type": "pwa-node", "request": "attach",
			"address": net.App.String(), "port": NodeInspectPort,
			"cwd":                       path.Join("/src", d.Cwd),
			"sourceMaps":                true,
			"outFiles":                  []string{"/src/**/*.(m|c|)js", "/out/**/*.(m|c|)js", "!**/node_modules/**"},
			"resolveSourceMapLocations": []string{"/src/**", "/out/**", "!**/node_modules/**"},
			"skipFiles":                 []string{"<node_internals>/**"},
			"continueOnAttach":          true,
		},
	}, nil
}

// EditorConfig attaches VS Code to js-debug in the adapter through the
// console's editor port: debugServer points VS Code at it instead of its
// own js-debug, and LocalRootKey tells the console the workspace folder it
// maps to /src (VS Code fills ${workspaceFolder} before it sends the
// request). The console adds debugServer to each child session js-debug
// opens, so VS Code connects those to the same port.
func (*NodeDebug) EditorConfig(l DebugLaunch, port int) map[string]any {
	cfg := maps.Clone(l.Arguments)
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["name"] = "aiman harness (Node)"
	cfg["debugServer"] = port
	cfg[LocalRootKey] = "${workspaceFolder}"
	return cfg
}
