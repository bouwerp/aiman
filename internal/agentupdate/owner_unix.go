//go:build unix

package agentupdate

import (
	"os"
	"syscall"
)

// OwnedByRoot reports whether uid 0 owns path. A path that cannot be read
// is not root-owned.
func OwnedByRoot(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0
}
