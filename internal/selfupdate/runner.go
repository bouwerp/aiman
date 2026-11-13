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
	// OpsDir holds operation results (<OpsDir>/<name>.result.json). Empty
	// refuses operations.
	OpsDir string

	tmpDir, logPath, resultPath string
}

// NewScriptRunner returns a runner writing scripts under tmpDir, appending
// their output to logPath and their Result to resultPath.
func NewScriptRunner(tmpDir, logPath, resultPath string) *ScriptRunner {
	return &ScriptRunner{Command: exec.Command, tmpDir: tmpDir, logPath: logPath, resultPath: resultPath}
}

// WrapperPath is <tmpDir>/host-update-<tag>.wrapper.sh for an install, and
// <tmpDir>/<name>.wrapper.sh for an operation.
func (r *ScriptRunner) WrapperPath(tag string) string {
	return filepath.Join(r.tmpDir, r.base(tag)+".wrapper.sh")
}

func (r *ScriptRunner) scriptPath(tag string) string {
	return filepath.Join(r.tmpDir, r.base(tag)+".sh")
}

func (r *ScriptRunner) base(tag string) string {
	if IsOperation(tag) {
		return tag
	}
	return "host-update-" + tag
}

// OperationResultPath is where the wrapper of operation name writes its
// Result.
func OperationResultPath(opsDir, name string) string {
	return filepath.Join(opsDir, name+".result.json")
}

// OperationLogPath is operation name's log beside the update log.
func OperationLogPath(updateLog, name string) string {
	return filepath.Join(filepath.Dir(updateLog), name+".log")
}

// paths returns the log and result files for tag: the update's, or an
// operation's own, so the two never mix.
func (r *ScriptRunner) paths(tag string) (logPath, resultPath string, err error) {
	if IsOperation(tag) {
		if r.OpsDir == "" {
			return "", "", fmt.Errorf("host operation %s: no operations directory", tag)
		}
		return OperationLogPath(r.logPath, tag), OperationResultPath(r.OpsDir, tag), nil
	}
	if _, ok := remotesvc.ReleaseTag(tag); !ok {
		return "", "", fmt.Errorf("host update: invalid release tag %q", tag)
	}
	return r.logPath, r.resultPath, nil
}

// Argv returns the argv Start uses: nohup bash <wrapper path>.
func (r *ScriptRunner) Argv(tag string) []string {
	return []string{"nohup", "bash", r.WrapperPath(tag)}
}

// wrapper runs the script and renames its Result into place whatever the exit.
// It has no set -e, so a failed script still gets a Result, and the rename
// means a reader never sees half a file.
func (r *ScriptRunner) wrapper(tag, resultPath string) string {
	result := shellx.Quote(resultPath)
	tmp := shellx.Quote(resultPath + ".tmp")
	return fmt.Sprintf(`#!/bin/sh
bash %s; ec=$?
printf '{"tag":"%%s","exit":%%d,"at":"%%s"}\n' %s "$ec" "$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)" > %s && mv -f %s %s
exit $ec
`, shellx.Quote(r.scriptPath(tag)), shellx.Quote(tag), tmp, tmp, result)
}

// Start writes the script (mode 0600) and its wrapper and starts the wrapper
// detached, with stdout and stderr appended to the log. tag is a release tag
// (vX.Y.Z), or one of the fixed operation names, whose log and Result are
// their own. It returns the wrapper's PID.
func (r *ScriptRunner) Start(tag, script string) (int, error) {
	logPath, resultPath, err := r.paths(tag)
	if err != nil {
		return 0, err
	}
	for _, dir := range []string{r.tmpDir, filepath.Dir(logPath), filepath.Dir(resultPath)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return 0, err
		}
	}
	if err := writeFile0600(r.scriptPath(tag), script); err != nil {
		return 0, err
	}
	if err := writeFile0600(r.WrapperPath(tag), r.wrapper(tag, resultPath)); err != nil {
		return 0, err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304: the caller owns the log path
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
