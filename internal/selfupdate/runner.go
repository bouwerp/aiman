package selfupdate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/bouwerp/aiman/internal/infra/remotesvc"
	"github.com/bouwerp/aiman/internal/shellx"
)

// ScriptRunner is the production Runner. It writes the script and a wrapper
// under tmpDir and starts `nohup bash <wrapper>` in a session of its own, so
// stopping serve (its process, group or, under KillMode=process, its unit)
// leaves the script running. The script body never reaches argv, where the
// serve pkill pattern in the script itself would match it.
type ScriptRunner struct {
	// Command is the process factory, exec.Command by default; tests replace it.
	Command func(name string, args ...string) *exec.Cmd

	tmpDir, logPath, resultPath string
}

// NewScriptRunner returns a runner writing scripts under tmpDir, appending
// their output to logPath and their Result to resultPath.
func NewScriptRunner(tmpDir, logPath, resultPath string) *ScriptRunner {
	return &ScriptRunner{Command: exec.Command, tmpDir: tmpDir, logPath: logPath, resultPath: resultPath}
}

// WrapperPath is <tmpDir>/host-update-<tag>.wrapper.sh.
func (r *ScriptRunner) WrapperPath(tag string) string {
	return filepath.Join(r.tmpDir, "host-update-"+tag+".wrapper.sh")
}

func (r *ScriptRunner) scriptPath(tag string) string {
	return filepath.Join(r.tmpDir, "host-update-"+tag+".sh")
}

// Argv returns the argv Start uses: nohup bash <wrapper path>.
func (r *ScriptRunner) Argv(tag string) []string {
	return []string{"nohup", "bash", r.WrapperPath(tag)}
}

// wrapper runs the script and renames its Result into place whatever the exit.
// It has no set -e, so a failed script still gets a Result, and the rename
// means a reader never sees half a file.
func (r *ScriptRunner) wrapper(tag string) string {
	result := shellx.Quote(r.resultPath)
	tmp := shellx.Quote(r.resultPath + ".tmp")
	return fmt.Sprintf(`#!/bin/sh
bash %s; ec=$?
printf '{"tag":"%%s","exit":%%d,"at":"%%s"}\n' %s "$ec" "$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)" > %s && mv -f %s %s
exit $ec
`, shellx.Quote(r.scriptPath(tag)), shellx.Quote(tag), tmp, tmp, result)
}

// Start writes the script (mode 0600) and its wrapper and starts the wrapper
// detached, with stdout and stderr appended to the log. It returns the
// wrapper's PID.
func (r *ScriptRunner) Start(tag, script string) (int, error) {
	if _, ok := remotesvc.ReleaseTag(tag); !ok {
		return 0, fmt.Errorf("host update: invalid release tag %q", tag)
	}
	if err := os.MkdirAll(r.tmpDir, 0o700); err != nil {
		return 0, err
	}
	if err := writeFile0600(r.scriptPath(tag), script); err != nil {
		return 0, err
	}
	if err := writeFile0600(r.WrapperPath(tag), r.wrapper(tag)); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(r.logPath), 0o700); err != nil {
		return 0, err
	}
	log, err := os.OpenFile(r.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304: the caller owns the log path
	if err != nil {
		return 0, err
	}
	defer func() { _ = log.Close() }()
	argv := r.Argv(tag)
	cmd := r.Command(argv[0], argv[1:]...)
	cmd.Stdin = nil
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("host update: start %s: %w", r.WrapperPath(tag), err)
	}
	pid := cmd.Process.Pid
	// Reap the wrapper if it ends while this process lives, so Alive does not
	// see a zombie.
	go func() { _ = cmd.Wait() }()
	return pid, nil
}

// writeFile0600 replaces path with content at mode 0600, whatever mode an
// earlier file had.
func writeFile0600(path, content string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}
