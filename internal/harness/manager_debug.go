package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness/edge"
)

// harness.debug ops. Take over and release both close the current DAP
// client at the edge: take over so the caller can connect next, release to
// free the debugger. The adapter restarts for the next client.
const (
	DebugStatus  = "status"
	DebugTake    = "take"
	DebugRelease = "release"
)

// DebugState is a debug run's debugger: who holds it, how to launch the app
// and the host port the edge publishes it on.
type DebugState struct {
	Run      string          `json:"run"`
	Holder   *edge.DAPHolder `json:"holder"`
	Dropped  *edge.DAPHolder `json:"dropped,omitempty"`
	Launch   *DebugLaunch    `json:"launch"`
	HostPort int             `json:"host_port"`
	Refused  int64           `json:"refused"`
}

// Debug answers a debug run's debugger state, or drops its client for take
// over or release.
func (m *Manager) Debug(ctx context.Context, id, op string, actor domain.EventActor) (DebugState, error) {
	if op != DebugStatus && op != DebugTake && op != DebugRelease {
		return DebugState{}, paramsError(fmt.Sprintf("op %q must be status, take or release", op))
	}
	m.mu.Lock()
	lr := m.live[id]
	m.mu.Unlock()
	if lr == nil {
		return DebugState{}, notRunning(id)
	}
	lr.mu.Lock()
	run, token := lr.run, lr.token
	lr.mu.Unlock()
	if !run.Debug {
		return DebugState{}, paramsError(fmt.Sprintf("run %s is not a debug run; start one with debug", id))
	}
	if run.State != StateReady {
		return DebugState{}, &Error{Code: CodeNotRunning, Msg: fmt.Sprintf("debug run %s is %s, not ready", id, run.State)}
	}
	st := DebugState{Run: id, Launch: run.DebugLaunch}
	for _, p := range run.Ports {
		if p.Name == RelayDAP {
			st.HostPort = p.HostPort
		}
	}
	sock := filepath.Join(m.runDir(run.Session, id), "control", ControlSocketName)
	if op != DebugStatus {
		dropped, err := m.opts.Edge.DAPDrop(ctx, sock, token, RelayDAP)
		if err != nil {
			return DebugState{}, fmt.Errorf("edge: %w", err)
		}
		st.Dropped = dropped
		name := ""
		if dropped != nil {
			name = dropped.ClientName
		}
		m.record(run.Session, actor, domain.EventHarnessDebug, map[string]any{"run": id, "op": op, "dropped": name})
	}
	h, err := m.opts.Edge.Health(ctx, sock, token)
	if err != nil {
		return DebugState{}, fmt.Errorf("edge: %w", err)
	}
	for _, r := range h.Relays {
		if r.Name == RelayDAP {
			st.Holder, st.Refused = r.Holder, r.Refused
		}
	}
	return st, nil
}

// SourceDir is the run's exported tree on the host. A kept run loses it
// when serve prunes old run directories.
func (m *Manager) SourceDir(id string) (Run, string, error) {
	run, err := m.Get(id)
	if err != nil {
		return Run{}, "", err
	}
	dir := filepath.Join(m.runDir(run.Session, id), "src")
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return Run{}, "", &Error{Code: CodeNotRunning, Msg: fmt.Sprintf("the source tree of run %s is gone", id)}
	}
	return run, dir, nil
}
