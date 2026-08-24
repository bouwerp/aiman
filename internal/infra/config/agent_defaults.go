package config

import "strings"

// LaunchDefaultsFor returns configured model/effort for an agent command or name.
func (c *Config) LaunchDefaultsFor(name, command string) AgentDefaults {
	if c == nil || len(c.AgentDefaults) == 0 {
		return AgentDefaults{}
	}
	if fields := strings.Fields(strings.ToLower(strings.TrimSpace(command))); len(fields) > 0 {
		if d, ok := c.AgentDefaults[fields[0]]; ok {
			return d
		}
	}
	n := strings.ToLower(name)
	for k, d := range c.AgentDefaults {
		if k != "" && strings.Contains(n, k) {
			return d
		}
	}
	return AgentDefaults{}
}

// AWSLocalProfileAllowed reports whether a local ~/.aws profile may be used.
// Omitted include_profiles allows every name; an explicit empty list allows none.
func (c *Config) AWSLocalProfileAllowed(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if c == nil || c.AWS.IncludeProfiles == nil {
		return true
	}
	for _, p := range *c.AWS.IncludeProfiles {
		if strings.EqualFold(strings.TrimSpace(p), name) {
			return true
		}
	}
	return false
}

// LocalSourceProfile is the ~/.aws profile used to mint this delegation.
// source_profile wins; otherwise the remote profile name (or "default").
func (d *AWSDelegation) LocalSourceProfile() string {
	if d == nil {
		return ""
	}
	if s := strings.TrimSpace(d.SourceProfile); s != "" {
		return s
	}
	if p := strings.TrimSpace(d.Profile); p != "" {
		return p
	}
	return "default"
}
