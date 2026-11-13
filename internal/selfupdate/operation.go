package selfupdate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/infra/remotesvc"
)

// Operation names: the only ones StartOperation and ScriptRunner accept.
const (
	OpRestartServe   = "restart-serve"
	OpRestartGateway = "restart-gateway"
	OpRestartTrigger = "restart-trigger"
)

var operations = []string{OpRestartServe, OpRestartGateway, OpRestartTrigger}

// Operation refusal reasons.
const (
	ReasonBusy    = "busy"
	ReasonRunning = "running"
)

// IsOperation reports one of the fixed operation names.
func IsOperation(name string) bool { return slices.Contains(operations, name) }

// RestartOperation is the operation name restarting daemon k.
func RestartOperation(k remotesvc.Kind) string { return "restart-" + string(k) }

// OperationKind is the daemon an operation restarts.
func OperationKind(name string) (remotesvc.Kind, bool) {
	if !IsOperation(name) {
		return "", false
	}
	return remotesvc.Kind(strings.TrimPrefix(name, "restart-")), true
}

func (m *Manager) opScript(name string) string {
	if m.OpScript != nil {
		return m.OpScript(name)
	}
	k, _ := OperationKind(name)
	return remotesvc.RestartScript(k)
}

func (m *Manager) opLogPath(name string) string {
	if m.LogPath == "" {
		return ""
	}
	return OperationLogPath(m.LogPath, name)
}

// busySum is every Busy counter, with no deferral bookkeeping.
func (m *Manager) busySum() int {
	sum := 0
	for _, c := range m.Busy {
		if c != nil {
			sum += c()
		}
	}
	return sum
}

func started(a *Attempt) bool { return a != nil && a.Status == AttemptStarted }

// StartOperation runs operation name (a daemon restart) detached through the
// runner. It is refused for a name outside the fixed set, while an operation
// or an install is started (ReasonRunning), and while the Busy gate counts
// anything (ReasonBusy) unless force. The started operation is saved before
// the script runs, so a serve the restart replaces settles it, and no install
// starts until it has settled. It runs in any update mode.
func (m *Manager) StartOperation(name string, force bool) Outcome {
	out := Outcome{Target: name}
	if !IsOperation(name) {
		out.Reason = fmt.Sprintf("unknown operation %q", name)
		return out
	}
	if m.OpsDir == "" {
		out.Reason = "operations are not configured on this host"
		return out
	}
	var ev events
	m.opMu.Lock()
	s := m.load()
	ev.add(m.settleLocked(&s))
	m.settleOpLocked(&s)
	switch {
	case started(s.Operation) || started(s.Attempt):
		out.Reason = ReasonRunning
	case !force && m.busySum() > 0:
		out.Reason = ReasonBusy
	default:
		out.Reason = m.startOpLocked(&s, name)
		out.Accepted = out.Reason == ""
	}
	m.opMu.Unlock()
	m.fire(ev)
	if out.Accepted {
		go m.watch()
	}
	return out
}

// startOpLocked saves the started operation and starts its script; it
// returns why it could not, or "".
func (m *Manager) startOpLocked(s *State, name string) string {
	result := OperationResultPath(m.OpsDir, name)
	if err := os.Remove(result); err != nil && !errors.Is(err, fs.ErrNotExist) {
		m.logf("host-op: remove %s: %v", result, err)
	}
	op := &Attempt{At: m.now(), From: m.Version, To: name, Status: AttemptStarted, Kind: AttemptKindRestart}
	s.Operation = op
	if err := s.Save(m.StatePath); err != nil {
		op.Status, op.Error = AttemptFailed, "save state: "+err.Error()
		m.logf("host-op: %s not started: %s", name, op.Error)
		return op.Error
	}
	pid, err := m.Runner.Start(name, m.opScript(name))
	if err != nil {
		op.Status, op.Error = AttemptFailed, "start script: "+err.Error()
		m.save(*s)
		m.logf("host-op: %s not started: %s", name, op.Error)
		return op.Error
	}
	op.PID = pid
	m.save(*s)
	m.logf("host-op: %s started (pid %d)", name, pid)
	return ""
}

// settleOpLocked decides a started operation from its own Result file and
// saves the state when it settled.
func (m *Manager) settleOpLocked(s *State) {
	op := s.Operation
	if !started(op) || m.OpsDir == "" {
		return
	}
	path := OperationResultPath(m.OpsDir, op.To)
	res, ok, err := ReadResult(path)
	if err != nil {
		m.logf("host-op: read %s: %v", path, err)
	}
	valid := ok && res.Tag == op.To && !res.At.Before(op.At.Truncate(time.Second))
	switch {
	case valid && res.Exit == 0:
		op.Status, op.Exit, op.Error = AttemptOK, 0, ""
	case valid:
		op.Status, op.Exit = AttemptFailed, res.Exit
		op.Error = withLogLine(fmt.Sprintf("%s failed (exit %d)", op.To, res.Exit), m.opLogPath(op.To))
	case m.now().Sub(op.At) < ResultWait || Alive(op.PID):
		return
	default:
		op.Status, op.Error = AttemptFailed, "no result"
	}
	m.save(*s)
	if op.Status == AttemptOK {
		m.logf("host-op: %s ok", op.To)
	} else {
		m.logf("host-op: %s failed: %s", op.To, op.Error)
	}
}

// Operation settles a started operation and returns the last one, nil when
// none ran.
func (m *Manager) Operation() *Attempt {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	s := m.load()
	m.settleOpLocked(&s)
	return s.Operation
}
