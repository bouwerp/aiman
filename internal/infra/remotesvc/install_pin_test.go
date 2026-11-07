package remotesvc

import (
	"strings"
	"testing"
)

// The remote install must take install.sh from the release it installs, never
// from the moving main branch, and must pin install.sh to that same release.
func TestInstallPipeAtPinsReleaseTag(t *testing.T) {
	const tag = "v0.29.3"
	for _, k := range []Kind{KindServe, KindTrigger, KindGateway} {
		pipe := k.InstallPipeAt(tag)
		if strings.Contains(pipe, "/main/") || strings.Contains(pipe, "refs/heads") {
			t.Errorf("%s: install pipe fetches from a branch: %q", k.Binary(), pipe)
		}
		fetchesTagged := strings.Contains(pipe, "/"+tag+"/install.sh") ||
			strings.Contains(pipe, "/releases/download/"+tag+"/")
		if !fetchesTagged {
			t.Errorf("%s: install.sh not fetched from release %s: %q", k.Binary(), tag, pipe)
		}
		if !strings.Contains(pipe, "VERSION="+tag) && !strings.Contains(pipe, "--version "+tag) {
			t.Errorf("%s: install.sh not pinned to %s (VERSION= or --version): %q", k.Binary(), tag, pipe)
		}
		if k != KindServe && !strings.Contains(pipe, "BINARY_NAME="+k.Binary()) {
			t.Errorf("%s: install pipe lost BINARY_NAME: %q", k.Binary(), pipe)
		}
	}
}
