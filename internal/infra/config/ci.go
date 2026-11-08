package config

// CIConfig configures serve's PR watcher.
type CIConfig struct {
	// TrustedLogins are GitHub logins whose review comments are steered even
	// when their author association is not OWNER, MEMBER or COLLABORATOR:
	// review bots (NONE) and members of a private organisation (CONTRIBUTOR).
	TrustedLogins []string `yaml:"trusted_logins,omitempty"`
}
