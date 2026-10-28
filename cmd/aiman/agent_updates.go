package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/agentupdate"
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
	}
	if deps.EnsureHooks != nil {
		m.AfterUpdate = func(string) { deps.EnsureHooks() }
	}
	return m
}

// agentPathDirs are the per-user install prefixes agents use. Serve runs
// under systemd with a short PATH, so probes and updates add these.
var agentPathDirs = []string{
	".local/bin", ".kilo/bin", ".npm-global/bin", "bin", ".bun/bin",
	".local/share/pnpm", ".yarn/bin",
}

// pathExec runs commands with the agent install prefixes appended to PATH,
// unless the command sets PATH itself.
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
		dirs := []string{os.Getenv("PATH")}
		for _, d := range agentPathDirs {
			dirs = append(dirs, filepath.Join(p.home, d))
		}
		dirs = append(dirs, "/usr/local/bin")
		env["PATH"] = strings.Trim(strings.Join(dirs, ":"), ":")
		c.Env = env
	}
	return p.base.Run(ctx, c)
}
