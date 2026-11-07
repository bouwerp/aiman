package domain

import (
	"errors"
	"fmt"
	"strings"
)

// ValidBranchName reports whether name is a branch name git's
// `check-ref-format --branch` accepts and that cannot be read as an option.
// The lone "@" is refused too: git reads it as HEAD, not as a name.
func ValidBranchName(name string) error {
	switch {
	case name == "":
		return errors.New("branch name is empty")
	case name == "@", name == "HEAD":
		return fmt.Errorf("branch name %q is reserved", name)
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("branch name %q starts with '-'", name)
	case strings.HasSuffix(name, "."):
		return fmt.Errorf("branch name %q ends with '.'", name)
	case strings.Contains(name, ".."), strings.Contains(name, "@{"):
		return fmt.Errorf("branch name %q contains '..' or '@{'", name)
	}
	for i := range len(name) {
		if c := name[i]; c < 0x20 || c == 0x7f || strings.IndexByte(" ~^:?*[\\", c) >= 0 {
			return fmt.Errorf("branch name %q contains the forbidden character %q", name, c)
		}
	}
	for _, part := range strings.Split(name, "/") {
		switch {
		case part == "":
			return fmt.Errorf("branch name %q has an empty path component", name)
		case strings.HasPrefix(part, "."):
			return fmt.Errorf("branch name %q has a component starting with '.'", name)
		case strings.HasSuffix(part, ".lock"):
			return fmt.Errorf("branch name %q has a component ending in .lock", name)
		}
	}
	return nil
}
