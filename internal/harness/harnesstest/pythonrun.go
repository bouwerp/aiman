package harnesstest

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/tools"
)

// PythonImage is the glibc Python image of the entry test; PythonMuslImage
// (Alpine) runs the wrapped child, so the pure wheel is shown working on
// musl too.
const (
	PythonImage     = "python:3.13-slim"
	PythonMuslImage = "python:3.13-alpine"
)

// PythonBreakLine is the line of bump's body in PythonSource.
const PythonBreakLine = 6

// PythonSource is the entry debuggee: a loop calling bump.
const PythonSource = `# A tiny loop for the harness debugger test.
import time


def bump(c):
    nxt = {"n": c["n"] + 1}
    return nxt


count = {"n": 0}
while True:
    count = bump(count)
    time.sleep(0.1)
`

// Wrapped child fixtures: app.run is a shell script (a process outside
// Python) that starts $PYTHON_BIN child.py; child.py starts grandchild.py
// through $PYTHON_BIN too, which debugpy reports as a subprocess.
const (
	PythonChildLine      = 6
	PythonGrandchildLine = 5
	pythonStart          = `#!/bin/sh
"$PYTHON_BIN" /src/child.py
sleep 3600
`
	PythonChild = `import os
import subprocess
import time

def work(n):
    return n * 2


subprocess.Popen([os.environ["PYTHON_BIN"], "/src/grandchild.py"])
while True:
    work(1)
    time.sleep(0.1)
`
	PythonGrandchild = `import time


def leaf(n):
    return n + 1


while True:
    leaf(1)
    time.sleep(0.1)
`
)

const pythonEntryFile = `version: 1
app:
  image: ` + PythonImage + `
  run: python3 app.py
  debug: {lang: python, entry: app.py}
scenarios:
  - id: dbg
`

const pythonWrapFile = `version: 1
app:
  image: ` + PythonMuslImage + `
  run: sh start.sh
  debug: {lang: python, wrap_env: [PYTHON_BIN]}
scenarios:
  - id: dbg
`

var (
	pyToolsOnce          sync.Once
	debugpyDir, pyBinDir string
	pyToolsErr           error
)

// pythonTools are the pinned debugpy wheel and the wrapper, through the
// tools store in the user cache.
func pythonTools(t *testing.T) (string, string) {
	t.Helper()
	pyToolsOnce.Do(func() {
		pyToolsErr = func() error {
			dir, err := CacheDir()
			if err != nil {
				return err
			}
			store := &tools.Store{Dir: filepath.Join(dir, "tools")}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			spec, err := tools.Debugpy(tools.Pin{})
			if err != nil {
				return err
			}
			if debugpyDir, err = store.Ensure(ctx, spec); err != nil {
				return err
			}
			pyBinDir, err = store.EnsureFile(tools.DebugpyPythonFile, tools.DebugpyPython)
			return err
		}()
	})
	if pyToolsErr != nil {
		t.Fatal(pyToolsErr)
	}
	return debugpyDir, pyBinDir
}

// UpPythonDebugRun starts the entry debuggee under debugpy in PythonImage.
func UpPythonDebugRun(t *testing.T) *DebugRun {
	t.Helper()
	return upPythonRun(t, pythonEntryFile, PythonImage, map[string]string{"app.py": PythonSource})
}

// UpPythonWrapRun starts the wrapped child fixtures in PythonMuslImage.
func UpPythonWrapRun(t *testing.T) *DebugRun {
	t.Helper()
	return upPythonRun(t, pythonWrapFile, PythonMuslImage, map[string]string{
		"start.sh": pythonStart, "child.py": PythonChild, "grandchild.py": PythonGrandchild,
	})
}

func upPythonRun(t *testing.T, file, image string, src map[string]string) *DebugRun {
	t.Helper()
	NeedDocker(t)
	for _, img := range []string{BusyboxImage, image} {
		ensureImage(t, img)
	}
	debugpy, bin := pythonTools(t)
	aiman := edgeBinary(t)
	dir := t.TempDir()
	for _, d := range []string{"src", "out", "control"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range src {
		if err := os.WriteFile(filepath.Join(dir, "src", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	token := randomHex(t, 32)
	if err := os.WriteFile(filepath.Join(dir, "control", harness.ControlTokenName), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := harness.Parse([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	a := &harness.PythonDebug{}
	if err := a.Check(f); err != nil {
		t.Fatal(err)
	}
	ds, err := harness.PythonDebugSpec(debugpy, bin, f)
	if err != nil {
		t.Fatal(err)
	}
	inner, outer, dbg := randomSubnets(t)
	launch, err := a.Launch(f, harness.DebugNetOf(dbg), ds)
	if err != nil {
		t.Fatal(err)
	}
	spec := harness.RunSpec{
		RunID: "p" + randomHex(t, 5), Scenario: "dbg",
		Src: filepath.Join(dir, "src"), Out: filepath.Join(dir, "out"), ControlDir: filepath.Join(dir, "control"),
		AimanBin: aiman, UID: os.Getuid(), GID: os.Getgid(),
		AppImage: image, EdgeImage: BusyboxImage,
		InnerSubnet: inner, OuterSubnet: outer, ReadPassword: "ro-" + randomHex(t, 4),
		Debug: ds, DebugSubnet: dbg,
	}
	p, err := harness.Compose(f, spec)
	if err != nil {
		t.Fatal(err)
	}
	return startRun(t, dir, p, spec, launch, harness.DebugNetOf(dbg).App.String())
}

// PythonInit is the initialize request of a debugpy client that takes
// child sessions through startDebugging.
var PythonInit = map[string]any{
	"clientID": "test", "clientName": "harness test", "adapterID": "debugpy",
	"pathFormat": "path", "linesStartAt1": true, "columnsStartAt1": true, "supportsStartDebuggingRequest": true,
}

// StopAt sets a breakpoint at path:line on s (already attached and
// initialized), sends configurationDone, and returns the top frame once it
// stops there.
func StopAt(t *testing.T, s *Session, path string, line int) map[string]any {
	t.Helper()
	s.Must("setBreakpoints", map[string]any{
		"source":      map[string]any{"path": path},
		"breakpoints": []map[string]any{{"line": line}},
	})
	s.Must("configurationDone", map[string]any{})
	stopped := s.Event("stopped")
	body, _ := stopped["body"].(map[string]any)
	st := s.Must("stackTrace", map[string]any{"threadId": body["threadId"], "levels": 5})
	frames, _ := st["body"].(map[string]any)["stackFrames"].([]any)
	if len(frames) == 0 {
		t.Fatalf("no frames: %v", st)
	}
	top := frames[0].(map[string]any)
	if l, _ := top["line"].(float64); int(l) != line {
		t.Fatalf("top frame %v, want line %d of %s (stopped %v)", top, line, path, body)
	}
	return top
}
