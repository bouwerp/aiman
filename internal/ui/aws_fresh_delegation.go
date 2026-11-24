package ui

import (
	"strings"

	"github.com/bouwerp/aiman/internal/infra/config"
)

// loadDiskConfig reads ~/.aiman/config.yaml. Tests replace it.
var loadDiskConfig = config.Load

// freshDelegation returns the delegation as the config file holds it now, so
// a hand edit of regions or session_policy applies to the next mint without a
// TUI restart. The TUI saves every edit it makes, so the file is never older
// than the in-memory copy. The match is the remote's host and user, then the
// delegation's profile. When the file cannot be read or holds no match, d is
// returned unchanged.
func freshDelegation(remote config.Remote, d *config.AWSDelegation) *config.AWSDelegation {
	if d == nil {
		return nil
	}
	cfg, err := loadDiskConfig()
	if err != nil || cfg == nil {
		return d
	}
	profile := strings.TrimSpace(d.Profile)
	for _, r := range cfg.Remotes {
		if r.Host != remote.Host || r.User != remote.User {
			continue
		}
		for _, fd := range r.AllDelegations() {
			if fd != nil && strings.TrimSpace(fd.Profile) == profile {
				return fd
			}
		}
	}
	return d
}
