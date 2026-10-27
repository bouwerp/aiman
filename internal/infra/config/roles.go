package config

import "strings"

// roleReviewer matches domain.SessionRoleReviewer, the verifier session role.
const roleReviewer = "reviewer"

// RoleOverride picks the agent, model and effort for sessions with a role.
// Empty fields leave the usual choice in place.
type RoleOverride struct {
	Agent  string `yaml:"agent,omitempty"`
	Model  string `yaml:"model,omitempty"`
	Effort string `yaml:"effort,omitempty"`
}

// RolesConfig holds per-role overrides. Only the verifier has one; child
// agents come from agents.preferred.
type RolesConfig struct {
	Verifier RoleOverride `yaml:"verifier,omitempty"`
}

// For returns the override for a session role. Only "reviewer" has one.
func (r RolesConfig) For(role string) RoleOverride {
	if role == roleReviewer {
		return r.Verifier
	}
	return RoleOverride{}
}

// RoleAgent returns the agent configured for a session role, or "".
func (c *Config) RoleAgent(role string) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.Roles.For(role).Agent)
}

// ResolveLaunchDefaults layers model and effort for one launch: agent_defaults,
// then the repo's agent_overrides, then the verifier role. The role applies
// only to a reviewer and only when it names no agent or the one launched.
func (c *Config) ResolveLaunchDefaults(name, command, repo, role string) AgentDefaults {
	if c == nil {
		return AgentDefaults{}
	}
	d := c.LaunchDefaultsFor(name, command)
	if r, ok := LookupRepo(c.Repos, repo); ok {
		d = overlayDefaults(d, matchAgentDefaults(r.AgentOverrides, name, command))
	}
	if role == roleReviewer {
		ro := c.Roles.Verifier
		agent := strings.ToLower(strings.TrimSpace(ro.Agent))
		fields := strings.Fields(strings.ToLower(command))
		if agent == "" || (len(fields) > 0 && fields[0] == agent) {
			d = overlayDefaults(d, AgentDefaults{Model: ro.Model, Effort: ro.Effort})
		}
	}
	return d
}

func overlayDefaults(base, top AgentDefaults) AgentDefaults {
	if strings.TrimSpace(top.Model) != "" {
		base.Model = top.Model
	}
	if strings.TrimSpace(top.Effort) != "" {
		base.Effort = top.Effort
	}
	return base
}

// ReviewerCandidates lists reviewer agents in order: roles.verifier.agent,
// then verify.agents, without duplicates.
func (c *Config) ReviewerCandidates() []string {
	if c == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(a string) {
		a = strings.TrimSpace(a)
		if a == "" || seen[a] {
			return
		}
		seen[a] = true
		out = append(out, a)
	}
	add(c.Roles.Verifier.Agent)
	for _, a := range c.Verify.Agents {
		add(a)
	}
	return out
}
