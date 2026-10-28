package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/agentupdate"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// agentUpdateDeps are the seams serve's agent update manager runs through.
type agentUpdateDeps struct {
	Exec agentupdate.Executor
	HTTP agentupdate.Fetcher
	// EnsureHooks reinstalls agent hooks after a successful update, since an
	// update can replace the agent's config defaults.
	EnsureHooks func()
	Logf        func(format string, args ...any)
	// Home is where agents.move_prefix puts the user npm prefix.
	Home string
}

// newAgentUpdates builds the manager for agents.update. State lives in
// <dir>/agent-versions.json and the update log in
// <dir>/logs/agent-updates.log. An invalid mode falls back to notify, so a
// typo never turns on automatic updates, and an invalid window falls back to
// the default window.
func newAgentUpdates(cfg config.AgentUpdateConfig, dir string, deps agentUpdateDeps) *agentupdate.Manager {
	logf := deps.Logf
	if logf == nil {
		logf = log.Printf
	}
	pol, err := agentupdate.NewPolicy(cfg.ModeOrDefault(), cfg.WindowOrDefault(), cfg.Hold)
	if err != nil {
		logf("agent updates: %v", err)
		mode, merr := agentupdate.ParseMode(cfg.ModeOrDefault())
		if merr != nil {
			mode = agentupdate.ModeNotify
		}
		pol, _ = agentupdate.NewPolicy(string(mode), "", cfg.Hold)
	}
	m := &agentupdate.Manager{
		Prober:          &agentupdate.Prober{Exec: deps.Exec, HTTP: deps.HTTP},
		Runner:          agentupdate.NewRunner(deps.Exec, filepath.Join(dir, "logs", "agent-updates.log")),
		StatePath:       filepath.Join(dir, "agent-versions.json"),
		Policy:          pol,
		CheckEvery:      cfg.CheckEveryOrDefault(),
		FirstProbeDelay: agentupdate.DefaultFirstProbeDelay,
		Logf:            logf,
		Home:            deps.Home,
	}
	if deps.EnsureHooks != nil {
		m.AfterUpdate = func(string) { deps.EnsureHooks() }
	}
	return m
}

// agentNotifier is the part of notify.Recorder agent updates use.
type agentNotifier interface {
	Emit(ctx context.Context, sessionID, label string, kind domain.NotifyKind, detail string) error
}

// wireAgentUpdateNotify raises agent_update notifications with no session
// id. A new version is announced in notify mode, and in auto mode only for
// held agents, since auto mode reports the update it applies instead. Only
// automatic updates are reported; a manual one answers on the agents page.
func wireAgentUpdateNotify(ctx context.Context, m *agentupdate.Manager, n agentNotifier, host string) {
	emit := func(agent, text string) {
		if err := n.Emit(ctx, "", agent, domain.NotifyAgentUpdate, text); err != nil {
			log.Printf("agent updates: notify %s: %v", agent, err)
		}
	}
	m.OnNewVersion = func(a agentupdate.AgentState) {
		if m.Policy.Mode == agentupdate.ModeAuto && !m.Policy.Held(a.Agent) {
			return
		}
		emit(a.Agent, agentNewVersionText(a, host))
	}
	m.OnApplied = func(a agentupdate.Applied) {
		if a.Auto {
			emit(a.Agent, agentAppliedText(a))
		}
	}
}

// agentNewVersionText is the notification for a newly published version.
func agentNewVersionText(a agentupdate.AgentState, host string) string {
	return fmt.Sprintf("%s %s → %s on %s (press a on Agent API)", a.Agent, a.Installed, a.Latest, host)
}

// agentAppliedText reports one automatic update: the versions on success,
// the first line of the error on failure.
func agentAppliedText(a agentupdate.Applied) string {
	if !a.OK {
		msg := strings.TrimSpace(a.Err)
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = strings.TrimSpace(msg[:i])
		}
		return fmt.Sprintf("%s update failed: %s", a.Agent, msg)
	}
	if a.To == "" {
		return fmt.Sprintf("updated %s from %s", a.Agent, a.From)
	}
	return fmt.Sprintf("updated %s %s → %s", a.Agent, a.From, a.To)
}

// agentPathDirs are the per-user install prefixes agents use. Serve runs
// under systemd with a short PATH, so probes and updates add these.
var agentPathDirs = []string{
	".local/bin", ".kilo/bin", "bin", ".bun/bin",
	".local/share/pnpm", ".yarn/bin",
}

// pathExec runs commands with the agent install prefixes appended to PATH,
// unless the command sets PATH itself. The user npm prefix comes first, so an
// agent moved there by agents.move_prefix wins over a root-owned copy.
type pathExec struct {
	base agentupdate.Executor
	home string
}

func (p pathExec) Run(ctx context.Context, c agentupdate.Command) (string, error) {
	if _, ok := c.Env["PATH"]; !ok && p.home != "" {
		env := make(map[string]string, len(c.Env)+1)
		for k, v := range c.Env {
			env[k] = v
		}
		dirs := []string{filepath.Join(p.home, agentupdate.UserPrefixDir, "bin"), os.Getenv("PATH")}
		for _, d := range agentPathDirs {
			dirs = append(dirs, filepath.Join(p.home, d))
		}
		dirs = append(dirs, "/usr/local/bin")
		env["PATH"] = strings.Trim(strings.Join(dirs, ":"), ":")
		c.Env = env
	}
	return p.base.Run(ctx, c)
}
