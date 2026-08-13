package mutagen

import "strings"

// DefaultIgnores are the paths excluded from every session mirror unless the
// user overrides them. They are all large, regenerable, and machine-specific:
// syncing them costs minutes of transfer on a slow link and buys nothing, since
// the agent rebuilds them on the remote anyway.
//
// Patterns use mutagen's default (gitignore-like) syntax, so a bare name
// matches at any depth. Nothing here is ever propagated in either direction,
// so an ignored directory on the remote is left untouched rather than deleted.
//
// `.git` is deliberately absent: the local mirror must stay a working git
// checkout for the VS Code handoff and for local sparse-checkout repair.
var DefaultIgnores = []string{
	// JavaScript / TypeScript
	"node_modules",
	".next",
	".nuxt",
	".turbo",
	".parcel-cache",
	".yarn/cache",
	// Python
	".venv",
	"venv",
	"__pycache__",
	"*.pyc",
	".pytest_cache",
	".mypy_cache",
	".ruff_cache",
	// Compiled / packaged output
	"target",
	"dist",
	"build",
	"out",
	// Toolchain caches
	".gradle",
	".cache",
	".terraform",
	"coverage",
	// Editor / OS noise
	".DS_Store",
}

// resolveIgnores combines the default set with user-supplied patterns.
// A leading "!" on a user pattern drops that entry from the defaults, which is
// how a project that genuinely tracks its build output opts back in.
func resolveIgnores(extra []string, useDefaults bool) []string {
	var out []string
	removed := make(map[string]bool)
	var additions []string

	for _, p := range extra {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "!") {
			removed[strings.TrimSpace(strings.TrimPrefix(p, "!"))] = true
			continue
		}
		additions = append(additions, p)
	}

	if useDefaults {
		for _, p := range DefaultIgnores {
			if !removed[p] {
				out = append(out, p)
			}
		}
	}

	seen := make(map[string]bool, len(out))
	for _, p := range out {
		seen[p] = true
	}
	for _, p := range additions {
		if !seen[p] {
			out = append(out, p)
			seen[p] = true
		}
	}
	return out
}
