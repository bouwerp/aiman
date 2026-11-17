package harness

import (
	"bytes"
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/harness/tools"
)

const (
	testDebugpyDir = "/home/u/.aiman/harness/tools/debugpy-v1.8.22-linux-amd64"
	testPyBinDir   = "/home/u/.aiman/harness/tools/debugpy-python-0123456789ab"
)

func pythonSpec(t *testing.T, fileYAML string) (*File, RunSpec) {
	t.Helper()
	f, err := Parse([]byte(fileYAML))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&PythonDebug{}).Check(f); err != nil {
		t.Fatal(err)
	}
	ds, err := PythonDebugSpec(testDebugpyDir, testPyBinDir, f)
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec()
	spec.Debug = ds
	spec.DebugSubnet = netip.MustParsePrefix("10.232.1.0/29")
	return f, spec
}

func TestPythonPlan(t *testing.T) {
	file := debugFixture(`python, entry: svc/main.py, cwd: svc, runtime: .venv/bin/python, runtime_args: ["-X", "dev"]`)
	f, spec := pythonSpec(t, file)
	p, doc := buildDebug(t, file, spec)

	// debugpy runs in the app: no adapter container, and the relay dials the
	// app's debug address at debugpy's port.
	svcs, _ := doc["services"].(map[string]any)
	if _, ok := svcs[ServiceAdapter]; ok {
		t.Fatal("a python run has an adapter container")
	}
	app := svcOf(t, doc, ServiceApp)
	env, _ := app["environment"].(map[string]any)
	if env[InspectEnv] != "10.232.1.2:5678" {
		t.Fatalf("app %s = %v", InspectEnv, env[InspectEnv])
	}
	vols := strings.Join(strs(app["volumes"]), " ")
	for _, want := range []string{testDebugpyDir + ":" + DebugpyMount + ":ro", testPyBinDir + ":" + DebugpyBinMount + ":ro"} {
		if !strings.Contains(vols, want) {
			t.Fatalf("app volumes %s lack %s", vols, want)
		}
	}
	cmd := strings.Join(strs(app["command"]), " ")
	for _, want := range []string{
		`PYTHONPATH="` + DebugpyMount + `$${PYTHONPATH:+:$$PYTHONPATH}"`,
		"cd '/src/svc'",
		"echo $$$$ >" + debugpyLock + "/pid",
		`exec '/src/.venv/bin/python' '-X' 'dev' -m debugpy --listen "$$AIMAN_INSPECT" --wait-for-client '/src/svc/main.py'`,
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("app command lacks %q:\n%s", want, cmd)
		}
	}
	var dap *Relay
	for i := range p.Relays {
		if p.Relays[i].Kind == RelayKindDAP {
			dap = &p.Relays[i]
		}
	}
	if dap == nil || dap.Target != "10.232.1.2:5678" {
		t.Fatalf("dap relay %+v, want the app's debugpy", dap)
	}

	// The probe runs the runtime from the cwd.
	probe := strings.Join(spec.Debug.Probe.Command, " ")
	if !strings.Contains(probe, "command -v '/src/.venv/bin/python'") || !strings.Contains(probe, "cd '/src/svc'") {
		t.Fatalf("probe %s", probe)
	}

	// The launch attaches to debugpy with the identity mapping and takes
	// subprocesses as child sessions through the editor port.
	a := &PythonDebug{}
	l, err := a.Launch(f, DebugNetOf(spec.DebugSubnet), spec.Debug)
	if err != nil {
		t.Fatal(err)
	}
	if l.Request != "attach" || !l.Children || !l.EditorPort() || l.SourceRoot != "/src" {
		t.Fatalf("launch %+v", l)
	}
	conn, _ := l.Arguments["connect"].(map[string]any)
	if conn["host"] != "10.232.1.2" || conn["port"] != PythonDAPPort || l.Arguments["type"] != "debugpy" || l.Arguments["subProcess"] != true {
		t.Fatalf("launch arguments %v", l.Arguments)
	}
	ed, err := EditorConfig(l, 4711)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := ed["connect"].(map[string]any); c["host"] != "127.0.0.1" || c["port"] != 4711 {
		t.Fatalf("editor connect %v", ed["connect"])
	}
	pm, _ := ed["pathMappings"].([]map[string]string)
	if len(pm) != 1 || pm[0]["localRoot"] != "${workspaceFolder}" || pm[0]["remoteRoot"] != "/src" {
		t.Fatalf("editor pathMappings %v", ed["pathMappings"])
	}
	if _, ok := ed[LocalRootKey]; ok {
		t.Fatal("the editor port must not rewrite paths debugpy maps itself")
	}

	// Its own run is used as it is, after the holder's pid.
	_, spec2 := pythonSpec(t, debugFixture(`python, run: 'python3 -m debugpy --listen "$AIMAN_INSPECT" --wait-for-client -m svc'`))
	script := spec2.Debug.Command[2]
	if !strings.Contains(script, debugpyLock+"/pid\npython3 -m debugpy --listen") {
		t.Fatalf("run script:\n%s", script)
	}

	// The adapter is picked by lang.
	if got, err := DebugAdapterFor(f, []DebugAdapter{&GoDebug{}, a}); err != nil || got != a {
		t.Fatalf("DebugAdapterFor = %v, %v", got, err)
	}
}

func TestPythonProbe(t *testing.T) {
	const ok = "AIMAN_PY_PATH=/usr/local/bin/python3\nAIMAN_PY_VERSION=CPython 3.13.1\nAIMAN_DEBUGPY=ok\nAIMAN_MUSL=1\n"
	if err := CheckPythonProbe(ok); err != nil {
		t.Fatalf("musl CPython 3.13 refused: %v", err)
	}
	for name, tc := range map[string]struct{ out, reason string }{
		"missing": {"AIMAN_PY_PATH=\n", ReasonPythonVersion},
		"old":     {"AIMAN_PY_PATH=/usr/bin/python3\nAIMAN_PY_VERSION=CPython 3.8.18\nAIMAN_DEBUGPY=ok\n", ReasonPythonVersion},
		"py2":     {"AIMAN_PY_PATH=/usr/bin/python\nAIMAN_PY_VERSION=CPython 2.7.18\n", ReasonPythonVersion},
		"pypy":    {"AIMAN_PY_PATH=/usr/bin/pypy3\nAIMAN_PY_VERSION=PyPy 3.10.14\nAIMAN_DEBUGPY=ok\n", ReasonPythonVersion},
		"garbled": {"AIMAN_PY_PATH=/usr/bin/python3\nsh: python3: not found\n", ReasonPythonVersion},
		"import":  {"AIMAN_PY_PATH=/usr/bin/python3\nAIMAN_PY_VERSION=CPython 3.12.0\nAIMAN_DEBUGPY_ERR=ModuleNotFoundError: No module named '_socket'\n", ReasonDebugpy},
	} {
		err := CheckPythonProbe(tc.out)
		var he *Error
		if !errors.As(err, &he) || he.Code != CodeDebugUnsupported || he.Reason != tc.reason || he.Lang != "python" {
			t.Fatalf("%s: %v, want %s/%s", name, err, CodeDebugUnsupported, tc.reason)
		}
	}
}

func TestWrapEnv(t *testing.T) {
	// Only wrap_env: app.run runs as it is, with the variable pointing at the
	// wrapper in the app command alone, so build and setup never wait for a
	// debugger.
	f, spec := pythonSpec(t, debugFixture(`python, wrap_env: [PYTHON_BIN, FAKESNOW_PY]`))
	script := spec.Debug.Command[2]
	for _, want := range []string{
		"PYTHON_BIN=" + DebugpyWrapper + "\nexport PYTHON_BIN\n",
		"FAKESNOW_PY=" + DebugpyWrapper + "\nexport FAKESNOW_PY\n",
		DebugpyPythonEnv + "='python3'",
		"\n" + f.App.Run + "\n",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script lacks %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "-m debugpy") || strings.Contains(script, debugpyLock) {
		t.Fatalf("a wrap_env-only run starts debugpy itself:\n%s", script)
	}
	if len(spec.Debug.Env) != 0 {
		t.Fatalf("service environment %v; wrap_env belongs in the app command", spec.Debug.Env)
	}

	// wrap_env is python's, and never a variable the debugger needs.
	if _, err := Parse([]byte(debugFixture(`node, entry: a.js, wrap_env: [PYTHON_BIN]`))); err == nil {
		t.Fatal("wrap_env on node parsed")
	}
	for _, n := range []string{"PATH", "PYTHONPATH", "AIMAN_INSPECT", "AIMAN_X"} {
		f, err := Parse([]byte(debugFixture(`python, wrap_env: [` + n + `]`)))
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		if err := (&PythonDebug{}).Check(f); err == nil {
			t.Fatalf("wrap_env %s accepted", n)
		}
	}

	// The wrapper uses the mounted wheel, and one process at a time holds
	// the debug address.
	if !strings.Contains(string(tools.DebugpyPython), "PYTHONPATH="+DebugpyMount+"${PYTHONPATH:+:$PYTHONPATH}") {
		t.Fatal("the wrapper does not put the mounted debugpy on PYTHONPATH")
	}
	if !strings.Contains(string(tools.DebugpyPython), "${AIMAN_DEBUGPY_LOCK:-"+debugpyLock+"}") {
		t.Fatal("the wrapper and the debug command disagree on the lock")
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "debugpy-python")
	if err := os.WriteFile(wrapper, tools.DebugpyPython, 0o700); err != nil { //nolint:gosec // G306: a test script
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "python")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"$PYTHONPATH|$*\"\n"), 0o700); err != nil { //nolint:gosec // G306: a test script
		t.Fatal(err)
	}
	lock := filepath.Join(dir, "lock")
	run := func() string {
		cmd := exec.Command("sh", wrapper, "child.py", "a b") //nolint:gosec // G204: the test's own files
		cmd.Env = []string{"PATH=/usr/bin:/bin", "AIMAN_INSPECT=10.232.1.2:5678", DebugpyPythonEnv + "=" + fake, "AIMAN_DEBUGPY_LOCK=" + lock, "PYTHONPATH=/app"}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("wrapper: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	debugged := DebugpyMount + ":/app|-m debugpy --listen 10.232.1.2:5678 --wait-for-client child.py a b"
	plain := DebugpyMount + ":/app|child.py a b"
	// No holder: this one debugs. Its pid (the exec'd fake) has exited, so
	// the next call finds a stale holder and debugs too.
	if got := run(); got != debugged {
		t.Fatalf("first wrapped interpreter ran %q, want %q", got, debugged)
	}
	if got := run(); got != debugged {
		t.Fatalf("after a dead holder ran %q, want %q", got, debugged)
	}
	// A live holder: the next one runs plainly.
	if err := os.WriteFile(filepath.Join(lock, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := run(); got != plain {
		t.Fatalf("with a live holder ran %q, want %q", got, plain)
	}
}

func TestDebugpyTool(t *testing.T) {
	s, err := tools.Debugpy(tools.Pin{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != tools.DebugpyVersion || s.SHA256 != tools.DebugpySHA256 || s.Format != tools.FormatZip ||
		!strings.HasSuffix(s.URL, "/debugpy-1.8.22-py2.py3-none-any.whl") {
		t.Fatalf("spec %+v", s)
	}
	for rel, want := range map[string]bool{
		"debugpy/__main__.py": true,
		"debugpy/_vendored/pydevd/pydevd_attach_to_process/attach_linux_amd64.so":  false,
		"debugpy/_vendored/pydevd/_pydevd_bundle/pydevd_cython.cp39-win_amd64.pyd": false,
		"debugpy-1.8.22.dist-info/licenses/LICENSE":                                true,
		"debugpy-1.8.22.dist-info/RECORD":                                          false,
	} {
		if got := s.Keep(rel); got != want {
			t.Fatalf("keep %s = %v", rel, got)
		}
	}
	other := strings.Repeat("ab", 32)
	if _, err := tools.Debugpy(tools.Pin{Version: "v1.8.23", SHA256: other}); err == nil {
		t.Fatal("another version without a url was accepted")
	}
	s, err = tools.Debugpy(tools.Pin{Version: "v1.8.23", SHA256: other, URL: "https://files.pythonhosted.org/packages/x/debugpy-1.8.23-py2.py3-none-any.whl"})
	if err != nil || s.Version != "v1.8.23" || s.SHA256 != other {
		t.Fatalf("override %+v, %v", s, err)
	}

	store := &tools.Store{Dir: t.TempDir()}
	dir, err := store.EnsureFile(tools.DebugpyPythonFile, tools.DebugpyPython)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, tools.DebugpyPythonFile))
	if err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("wrapper %v, %v", fi, err)
	}
	if err := os.WriteFile(filepath.Join(dir, tools.DebugpyPythonFile), []byte("changed"), 0o700); err != nil { //nolint:gosec // G306: a test file
		t.Fatal(err)
	}
	again, err := store.EnsureFile(tools.DebugpyPythonFile, tools.DebugpyPython)
	if err != nil || again != dir {
		t.Fatalf("again %s, %v", again, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, tools.DebugpyPythonFile)); !bytes.Equal(b, tools.DebugpyPython) { //nolint:gosec // G304: the test's own file
		t.Fatal("a changed wrapper was not written again")
	}
}
