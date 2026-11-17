package harness

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bouwerp/aiman/internal/harness/tools"
	"github.com/bouwerp/aiman/internal/shellx"
)

// Python. debugpy is both the debugger and its DAP server, so like Delve it
// runs in the app container and the edge relays DAP to the app's debug
// address; there is no adapter container. The pinned pure-Python wheel is
// mounted read-only and put on PYTHONPATH, so the image needs only CPython
// 3.9 or later (glibc or musl: the wheel has no compiled speedups). The app
// runs `<runtime> <runtime_args> -m debugpy --listen $AIMAN_INSPECT
// --wait-for-client /src/<entry>`, or the file's own run. Each variable in
// wrap_env points at debugpy-python, a wrapper that starts the interpreter a
// process outside Python (a test runner, a Node server) launches under
// debugpy instead. debugpy reports each Python subprocess of a debugged
// interpreter through the startDebugging reverse request, and the child
// session connects to the same address under the client's lease.
const (
	// PythonDAPPort is debugpy's DAP port on the app's debug address.
	PythonDAPPort = 5678
	// DebugpyMount is the unpacked wheel, the directory on PYTHONPATH.
	DebugpyMount = ToolsMount + "/debugpy"
	// DebugpyBinMount holds the wrap_env wrapper.
	DebugpyBinMount = ToolsMount + "/bin"
	// DebugpyWrapper is the wrapper a wrap_env variable names.
	DebugpyWrapper = DebugpyBinMount + "/" + tools.DebugpyPythonFile
	// DebugpyPythonEnv names the interpreter the wrapper runs.
	DebugpyPythonEnv = "AIMAN_DEBUGPY_PYTHON"
	// debugpyLock is the directory whose pid file names the process that
	// holds the debug address; the wrapper reads it.
	debugpyLock = "/tmp/aiman-debugpy"
	// minPythonMajor and minPythonMinor are the oldest CPython debugpy 1.8
	// supports.
	minPythonMajor, minPythonMinor = 3, 9
)

// Reasons of a CodeDebugUnsupported refusal for python.
const (
	// ReasonPythonVersion is an image with no CPython 3.9 or later.
	ReasonPythonVersion = "python_version"
	// ReasonDebugpy is an interpreter that cannot import the mounted
	// debugpy.
	ReasonDebugpy = "debugpy"
)

// wrapEnvReserved are variables wrap_env may not point at the wrapper: the
// wrapper and the debug command need them as they are.
var wrapEnvReserved = []string{"PATH", "HOME", "PYTHONPATH", "PYTHONHOME", "SHELL", InspectEnv, DebugpyPythonEnv}

// PythonDebug is the Python adapter.
type PythonDebug struct {
	// Tools keeps the pinned debugpy wheel and the wrapper on the host.
	Tools *tools.Store
	// Debugpy overrides the pin (harness.debug_tools.debugpy).
	Debugpy tools.Pin
}

// Lang is python.
func (*PythonDebug) Lang() string { return "python" }

// Check accepts a python block whose command can be built and whose
// wrap_env names no variable the debug command needs.
func (*PythonDebug) Check(f *File) error {
	if f.App.Debug == nil || f.App.Debug.Lang != "python" {
		return invalid("the python debugger needs app.debug with lang python")
	}
	d := f.App.Debug
	for _, n := range d.WrapEnv {
		if slices.Contains(wrapEnvReserved, n) || strings.HasPrefix(n, "AIMAN_") {
			return invalid(fmt.Sprintf("app.debug.wrap_env %q is a variable the debugger needs; name the variable that holds the Python interpreter (for example PYTHON_BIN)", n))
		}
	}
	if d.Entry == "" && d.Run == "" && strings.TrimSpace(f.App.Run) == "" {
		return invalid("app.debug: python with only wrap_env runs app.run, which is empty")
	}
	_, err := pythonScript(f)
	return err
}

// AdapterImage is empty: debugpy runs in the app container.
func (*PythonDebug) AdapterImage() string { return "" }

// Spec fetches or verifies the debugpy wheel, writes the wrapper, and runs
// the app under debugpy.
func (p *PythonDebug) Spec(ctx context.Context, f *File) (*DebugSpec, error) {
	if p.Tools == nil {
		return nil, errors.New("the Python debugger has no tools directory")
	}
	spec, err := tools.Debugpy(p.Debugpy)
	if err != nil {
		return nil, err
	}
	dir, err := p.Tools.Ensure(ctx, spec)
	if err != nil {
		return nil, toolError(err)
	}
	bin, err := p.Tools.EnsureFile(tools.DebugpyPythonFile, tools.DebugpyPython)
	if err != nil {
		return nil, err
	}
	return PythonDebugSpec(dir, bin, f)
}

// PythonDebugSpec runs the app under debugpy from debugpyDir, with the
// wrapper in binDir for wrap_env.
func PythonDebugSpec(debugpyDir, binDir string, f *File) (*DebugSpec, error) {
	d := f.App.Debug
	if d == nil {
		return nil, invalid("the python debugger needs app.debug")
	}
	script, err := pythonScript(f)
	if err != nil {
		return nil, err
	}
	return &DebugSpec{
		Lang:    "python",
		Command: []string{"sh", "-c", script},
		Mounts:  []ToolMount{{Host: debugpyDir, Container: DebugpyMount}, {Host: binDir, Container: DebugpyBinMount}},
		Port:    PythonDAPPort,
		Probe: &DebugProbe{
			Command: []string{"sh", "-c", pythonProbeScript(d)},
			Check:   CheckPythonProbe,
		},
	}, nil
}

// pythonEnv is the environment every debugpy process needs: the wheel first
// on PYTHONPATH, and pydevd told not to look for compiled speedups the pure
// wheel lacks.
const pythonEnv = `PYTHONPATH="` + DebugpyMount + `${PYTHONPATH:+:$PYTHONPATH}"
PYDEVD_USE_CYTHON=NO
PYDEVD_USE_FRAME_EVAL=NO
PYDEVD_DISABLE_FILE_VALIDATION=1
export PYTHONPATH PYDEVD_USE_CYTHON PYDEVD_USE_FRAME_EVAL PYDEVD_DISABLE_FILE_VALIDATION
`

// pythonScript is the app command of a python debug run. The wrap_env
// variables are set here, not in the service's environment, so app.build,
// setup and fixtures never start an interpreter that waits for a debugger.
// With entry or run, the command records its pid as the debug address's
// holder, so a wrapped interpreter it starts runs without a second server
// (debugpy's subprocess support still debugs it when this interpreter
// starts it). With only wrap_env, app.run runs as it is.
func pythonScript(f *File) (string, error) {
	d := f.App.Debug
	rt := debugRuntime(d, "python3")
	var b strings.Builder
	b.WriteString(pythonEnv)
	b.WriteString(DebugpyPythonEnv + "=" + shellx.Quote(rt) + "\nexport " + DebugpyPythonEnv + "\n")
	for _, n := range d.WrapEnv {
		b.WriteString(n + "=" + DebugpyWrapper + "\nexport " + n + "\n")
	}
	b.WriteString("cd " + shellx.Quote(path.Join("/src", d.Cwd)) + " || exit 1\n")
	switch {
	case d.Run != "":
		b.WriteString(pythonHold)
		b.WriteString(d.Run + "\n")
	case d.Entry != "":
		b.WriteString(pythonHold)
		words := []string{shellx.Quote(rt)}
		for _, a := range d.RuntimeArgs {
			words = append(words, shellx.Quote(a))
		}
		words = append(words, "-m", "debugpy", "--listen", `"$`+InspectEnv+`"`, "--wait-for-client", shellx.Quote(path.Join("/src", d.Entry)))
		b.WriteString("exec " + strings.Join(words, " ") + "\n")
	case len(d.WrapEnv) > 0:
		b.WriteString(f.App.Run + "\n")
	default:
		return "", invalid("app.debug: python needs entry, run or wrap_env")
	}
	return b.String(), nil
}

// pythonHold records this shell (the debug command execs in it, or runs
// under it) as the debug address's holder.
const pythonHold = `rm -rf ` + debugpyLock + ` && mkdir ` + debugpyLock + ` && echo $$ >` + debugpyLock + `/pid
`

// Probe output lines.
const (
	probePyPath    = "AIMAN_PY_PATH="
	probePyVersion = "AIMAN_PY_VERSION="
	probeDebugpyOK = "AIMAN_DEBUGPY=ok"
	probeDebugpyEr = "AIMAN_DEBUGPY_ERR="
)

// pythonVersionCode prints the implementation and version; it runs on
// Python 2 as well, so an old interpreter is named rather than failing.
const pythonVersionCode = `import sys, platform; print("` + probePyVersion + `%s %d.%d.%d" % ((platform.python_implementation(),) + tuple(sys.version_info[:3])))`

// pythonProbeScript reports the interpreter app.debug.runtime (else
// python3) names from app.debug.cwd, its implementation and version, whether
// it imports the mounted debugpy, and musl libc (shown, never refused).
func pythonProbeScript(d *Debug) string {
	rt := shellx.Quote(debugRuntime(d, "python3"))
	return `cd ` + shellx.Quote(path.Join("/src", d.Cwd)) + ` || exit 1
p=$(command -v ` + rt + `) || p=
echo "` + probePyPath + `$p"
[ -n "$p" ] || exit 0
"$p" -c ` + shellx.Quote(pythonVersionCode) + ` 2>&1 | head -n 3
if out=$(PYTHONPATH=` + DebugpyMount + ` PYDEVD_USE_CYTHON=NO PYDEVD_USE_FRAME_EVAL=NO "$p" -c 'import debugpy.server.cli' 2>&1); then
  echo ` + probeDebugpyOK + `
else
  printf '%s\n' "$out" | tail -n 3 | sed 's/^/` + probeDebugpyEr + `/'
fi
if ls /lib/ld-musl-* >/dev/null 2>&1; then echo ` + probeMusl + `; fi
`
}

var pyVersionRe = regexp.MustCompile(`^(\S+) (\d+)\.(\d+)\.(\d+)$`)

// CheckPythonProbe reads the probe's output: the interpreter must exist, be
// CPython 3.9 or later, and import the mounted debugpy.
func CheckPythonProbe(out string) error {
	var py, version string
	var importErr []string
	ok := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, probePyPath):
			py = strings.TrimPrefix(line, probePyPath)
		case strings.HasPrefix(line, probePyVersion):
			version = strings.TrimPrefix(line, probePyVersion)
		case line == probeDebugpyOK:
			ok = true
		case strings.HasPrefix(line, probeDebugpyEr):
			importErr = append(importErr, strings.TrimPrefix(line, probeDebugpyEr))
		}
	}
	if py == "" {
		return pythonVersionError("the app container has no Python interpreter for the debugger (app.debug.runtime, else python3)")
	}
	m := pyVersionRe.FindStringSubmatch(version)
	if m == nil {
		return pythonVersionError(fmt.Sprintf("%s did not report a Python version (probe output %q)", py, tailLines(out, 5)))
	}
	major, _ := strconv.Atoi(m[2])
	minor, _ := strconv.Atoi(m[3])
	if m[1] != "CPython" || major < minPythonMajor || (major == minPythonMajor && minor < minPythonMinor) {
		return pythonVersionError(fmt.Sprintf("the app runs %s %s.%s.%s (%s)", m[1], m[2], m[3], m[4], py))
	}
	if !ok {
		return &Error{Code: CodeDebugUnsupported, Lang: "python", Reason: ReasonDebugpy,
			Msg: py + " cannot import the mounted debugpy: " + strings.Join(importErr, "; ")}
	}
	return nil
}

func pythonVersionError(what string) *Error {
	return &Error{Code: CodeDebugUnsupported, Lang: "python", Reason: ReasonPythonVersion,
		Msg: fmt.Sprintf("%s; debugpy needs CPython %d.%d or later. Use an image with python3 %d.%d+, or name the interpreter in app.debug.runtime.",
			what, minPythonMajor, minPythonMinor, minPythonMajor, minPythonMinor)}
}

// Launch is an attach to debugpy on the app's debug address. debugpy runs in
// the app, where the tree is at /src, so the mapping is the identity; an
// editor maps its workspace folder instead.
func (*PythonDebug) Launch(f *File, net DebugNet, _ *DebugSpec) (DebugLaunch, error) {
	if f.App.Debug == nil {
		return DebugLaunch{}, invalid("the python debugger needs app.debug")
	}
	return DebugLaunch{
		Lang: "python", Adapter: "debugpy", Request: "attach", SourceRoot: "/src", Children: true,
		Arguments: map[string]any{
			"name": "aiman harness", "type": "debugpy", "request": "attach",
			"connect":      map[string]any{"host": net.App.String(), "port": PythonDAPPort},
			"pathMappings": []map[string]string{{"localRoot": "/src", "remoteRoot": "/src"}},
			"justMyCode":   true,
			"subProcess":   true,
		},
	}, nil
}

// EditorConfig attaches VS Code's Python debugger through the console's
// editor port: connect names that port, which adds the lease line, and
// debugpy repeats connect in each subprocess's startDebugging
// configuration, so VS Code connects the children there too. debugpy maps
// the workspace folder to /src itself (pathMappings), so the editor port
// rewrites no paths.
func (*PythonDebug) EditorConfig(l DebugLaunch, port int) map[string]any {
	cfg := map[string]any{
		"name": "aiman harness (Python)", "type": "debugpy", "request": "attach",
		"connect":      map[string]any{"host": "127.0.0.1", "port": port},
		"pathMappings": []map[string]string{{"localRoot": "${workspaceFolder}", "remoteRoot": l.SourceRoot}},
		"justMyCode":   true,
		"subProcess":   true,
	}
	return cfg
}
