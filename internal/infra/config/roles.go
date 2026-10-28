package config

import (
	"slices"
	"strings"
)

// Role names match domain.SessionRoleReviewer and domain.SessionRoleHandover.
const (
	roleReviewer = "reviewer"
	roleHandover = "handover"
)

// RoleOverride picks the agent, model and effort for sessions with a role.
// Empty fields leave the usual choice in place.
type RoleOverride struct {
	Agent  string `yaml:"agent,omitempty"`
	Model  string `yaml:"model,omitempty"`
	Effort string `yaml:"effort,omitempty"`
}

// RolesConfig holds per-role overrides for aiman's helper sessions. Child
// agents come from agents.preferred.
type RolesConfig struct {
	Verifier RoleOverride `yaml:"verifier,omitempty"`
	Handover RoleOverride `yaml:"handover,omitempty"`
}

// For returns the override for a session role: "reviewer" or "handover".
// Other roles have none.
func (r RolesConfig) For(role string) RoleOverride {
	switch role {
	case roleReviewer:
		return r.Verifier
	case roleHandover:
		return r.Handover
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
// then the repo's agent_overrides, then the role override (see RoleOverrideFor).
func (c *Config) ResolveLaunchDefaults(name, command, repo, role string) AgentDefaults {
	return c.ResolveLaunchDefaultsWith(name, command, repo, role, AgentDefaults{})
}

// ResolveLaunchDefaultsWith is ResolveLaunchDefaults with pick, the model and
// effort of an agents.preferred entry, layered between the repo's
// agent_overrides and the role override.
func (c *Config) ResolveLaunchDefaultsWith(name, command, repo, role string, pick AgentDefaults) AgentDefaults {
	if c == nil {
		return overlayDefaults(AgentDefaults{}, pick)
	}
	d := c.LaunchDefaultsFor(name, command)
	if r, ok := LookupRepo(c.Repos, repo); ok {
		d = overlayDefaults(d, matchAgentDefaults(r.AgentOverrides, name, command))
	}
	d = overlayDefaults(d, pick)
	ro := c.RoleOverrideFor(role, command)
	return overlayDefaults(d, AgentDefaults{Model: ro.Model, Effort: ro.Effort})
}

// RoleOverrideFor returns the role's override when it applies to this launch:
// the role names no agent, or names the command's binary. Otherwise it is zero.
func (c *Config) RoleOverrideFor(role, command string) RoleOverride {
	if c == nil {
		return RoleOverride{}
	}
	ro := c.Roles.For(role)
	agent := strings.ToLower(strings.TrimSpace(ro.Agent))
	fields := strings.Fields(strings.ToLower(command))
	if agent == "" || (len(fields) > 0 && fields[0] == agent) {
		return ro
	}
	return RoleOverride{}
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

// ReviewerCandidates lists reviewer entries in order: roles.verifier, then
// verify.agents, then the smart tier, without duplicates. The role entry
// carries the role's model and effort so the pick keeps them.
func (c *Config) ReviewerCandidates() []PreferredAgent {
	if c == nil {
		return nil
	}
	var out []PreferredAgent
	if role := c.Roles.Verifier; strings.TrimSpace(role.Agent) != "" {
		out = append(out, PreferredAgent(role))
	}
	for _, name := range c.Verify.Agents {
		out = append(out, PreferredAgent{Agent: name})
	}
	out = append(out, c.Agents.Smart...)
	return uniqueEntries(out)
}

// HandoverCandidates lists handover child entries in order: roles.handover,
// then the worker tier (with each entry's model), then the worker's own
// agent, without duplicates. The role entry carries the role's model and
// effort so the pick keeps them.
func (c *Config) HandoverCandidates(workerAgent string) []PreferredAgent {
	if c == nil {
		return uniqueEntries([]PreferredAgent{{Agent: workerAgent}})
	}
	var out []PreferredAgent
	if role := c.Roles.Handover; strings.TrimSpace(role.Agent) != "" {
		out = append(out, PreferredAgent(role))
	}
	out = append(out, c.Agents.Preferred...)
	out = append(out, PreferredAgent{Agent: workerAgent})
	return uniqueEntries(out)
}

// uniqueEntries trims each command, drops blanks and exact repeats (agent,
// model and effort), keeping order. The same agent may stay with a different
// model: that is the point of a per-entry model.
func uniqueEntries(entries []PreferredAgent) []PreferredAgent {
	var out []PreferredAgent
	for _, e := range entries {
		e.Agent = strings.TrimSpace(e.Agent)
		e.Model = strings.TrimSpace(e.Model)
		e.Effort = strings.TrimSpace(e.Effort)
		if e.Agent == "" || slices.Contains(out, e) {
			continue
		}
		out = append(out, e)
	}
	return out
}
