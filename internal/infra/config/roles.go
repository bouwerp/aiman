package config

import "strings"

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
	if c == nil {
		return AgentDefaults{}
	}
	d := c.LaunchDefaultsFor(name, command)
	if r, ok := LookupRepo(c.Repos, repo); ok {
		d = overlayDefaults(d, matchAgentDefaults(r.AgentOverrides, name, command))
	}
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

// ReviewerCandidates lists reviewer agents in order: roles.verifier.agent,
// then verify.agents, without duplicates.
func (c *Config) ReviewerCandidates() []string {
	if c == nil {
		return nil
	}
	return uniqueAgents(append([]string{c.Roles.Verifier.Agent}, c.Verify.Agents...))
}

// HandoverCandidates lists handover child agents in order: roles.handover.agent,
// then agents.preferred, then the worker's own agent, without duplicates.
func (c *Config) HandoverCandidates(workerAgent string) []string {
	if c == nil {
		return uniqueAgents([]string{workerAgent})
	}
	names := append([]string{c.Roles.Handover.Agent}, c.Agents.Preferred...)
	return uniqueAgents(append(names, workerAgent))
}

// uniqueAgents trims each name and drops blanks and repeats, keeping order.
func uniqueAgents(names []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range names {
		a = strings.TrimSpace(a)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}
