package awsdelegation

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ListLocalAWSProfileNames returns sorted unique profile names from ~/.aws/config
// and ~/.aws/credentials on this machine (the same names are typically mirrored on
// the remote as source_profile when credentials are synced).
func ListLocalAWSProfileNames() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var names []string
	add := func(n string) {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		names = append(names, n)
	}

	_ = parseAWSSectionFile(filepath.Join(home, ".aws", "config"), add, true)
	_ = parseAWSSectionFile(filepath.Join(home, ".aws", "credentials"), add, false)

	slices.Sort(names)
	return names, nil
}

// parseAWSSectionFile reads INI-style [section] headers. isConfig is true for ~/.aws/config
// (uses [profile x] and [default]); false for credentials ([name] only).
func parseAWSSectionFile(path string, add func(string), isConfig bool) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
			continue
		}
		inner := strings.TrimSpace(line[1 : len(line)-1])
		if isConfig {
			switch {
			case inner == "default":
				add("default")
			case strings.HasPrefix(inner, "profile "):
				add(strings.TrimSpace(strings.TrimPrefix(inner, "profile ")))
			}
			// Skip [sso-session …], [services …], etc.
			continue
		}
		add(inner)
	}
	return sc.Err()
}
