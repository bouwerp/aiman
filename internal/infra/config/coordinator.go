package config

// CoordinatorConfig configures how a coordinator's children deliver work.
type CoordinatorConfig struct {
	// ChildPRs makes children open a PR into the coordinator's branch. Unset
	// or false lands them with aiman land, which costs no CI run.
	ChildPRs bool `yaml:"child_prs,omitempty"`
}

// CoordinatorChildPRs reports whether children land through a PR. A nil
// config is false.
func (c *Config) CoordinatorChildPRs() bool {
	return c != nil && c.Coordinator.ChildPRs
}
