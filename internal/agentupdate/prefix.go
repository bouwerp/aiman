package agentupdate

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
)

// UserPrefixDir is the npm prefix, relative to $HOME, that MovePrefix moves
// an agent into. aiman puts its bin directory ahead of the system paths.
const UserPrefixDir = ".npm-global"

// MovePrefixCommands are the commands that reinstall e under the user npm
// prefix in home. They never uninstall the root-owned copy, which needs
// root; the user prefix wins because its bin directory comes first on PATH.
// The page shows them with home "~".
func MovePrefixCommands(home string, e Entry) []Command {
	prefix := path.Join(home, UserPrefixDir)
	if home == "~" {
		prefix = "~/" + UserPrefixDir
	}
	return []Command{
		{Argv: []string{"npm", "config", "set", "prefix", prefix}},
		{Argv: []string{"npm", "install", "-g", e.Package + "@latest"}},
	}
}

// CanMovePrefix reports whether a can move to the user npm prefix: an npm
// install in a root-owned prefix, or one whose last update needed root.
func CanMovePrefix(a AgentStatus) bool {
	if a.Method != MethodNPM || a.Installed == "" {
		return false
	}
	return a.RootOwned || a.Status == StatusNeedsRoot
}

// MovePrefix reinstalls agent under the user npm prefix in the background,
// then re-probes it. It refuses an agent that is not an npm install, has no
// package, is already updating, or is held by the gate.
func (m *Manager) MovePrefix(ctx context.Context, agent string) (Outcome, error) {
	agent = strings.TrimSpace(agent)
	e, ok := m.entry(agent)
	if !ok {
		return Outcome{}, fmt.Errorf("unknown agent %q", agent)
	}
	if e.Package == "" {
		return Outcome{}, fmt.Errorf("%s is not an npm package", agent)
	}
	if m.Home == "" {
		return Outcome{}, errors.New("home directory unknown")
	}
	m.ensureProbed(ctx, false)
	m.mu.Lock()
	a, installed := m.loadLocked().Agents[agent]
	m.mu.Unlock()
	if installed && a.Method != MethodNPM {
		return Outcome{Agent: agent, Reason: fmt.Sprintf("installed by %s, not npm", a.Method)}, nil
	}
	return m.startWith(agent, m.live(ctx), MovePrefixCommands(m.Home, e), true), nil
}
