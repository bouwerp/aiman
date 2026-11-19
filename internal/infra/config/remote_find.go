package config

import (
	"fmt"
	"strings"
)

// Target is how the TUI labels a remote: user@host, or host when no user is
// set.
func (r Remote) Target() string {
	if r.User == "" {
		return r.Host
	}
	return r.User + "@" + r.Host
}

// IsActive reports whether active (the config's active_remote) names r. The
// TUI writes a host there and the CLI reads a name, so either counts.
func (r Remote) IsActive(active string) bool {
	active = strings.TrimSpace(active)
	return active != "" && (strings.EqualFold(r.Name, active) || strings.EqualFold(r.Host, active))
}

// FindRemote finds the remote ref names: its name, ignoring case, first;
// else user@host (or a bare host) when exactly one remote has it. A miss
// names every configured remote, so the caller can see what to pass.
func FindRemote(remotes []Remote, ref string) (Remote, error) {
	ref = strings.TrimSpace(ref)
	for _, r := range remotes {
		if strings.EqualFold(r.Name, ref) {
			return r, nil
		}
	}
	var hits []Remote
	for _, r := range remotes {
		if r.Host != "" && (strings.EqualFold(r.Target(), ref) || strings.EqualFold(r.Host, ref)) {
			hits = append(hits, r)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return Remote{}, fmt.Errorf("no remote %s in the config; remotes: %s", ref, RemoteNames(remotes))
	}
	names := make([]string, 0, len(hits))
	for _, r := range hits {
		names = append(names, r.Name)
	}
	return Remote{}, fmt.Errorf("%s matches several remotes (%s); pass one name", ref, strings.Join(names, ", "))
}

// RemoteNames lists the remotes' names for an error message; "none" when
// the config has no remote.
func RemoteNames(remotes []Remote) string {
	if len(remotes) == 0 {
		return "none (add one in the TUI: aiman init)"
	}
	names := make([]string, 0, len(remotes))
	for _, r := range remotes {
		names = append(names, r.Name)
	}
	return strings.Join(names, ", ")
}
