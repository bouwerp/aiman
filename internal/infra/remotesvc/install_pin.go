package remotesvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"
)

const releaseDownloadBase = "https://github.com/bouwerp/aiman/releases/download/"

// latestReleaseURL is a variable so tests can point it at a local server.
var latestReleaseURL = "https://api.github.com/repos/bouwerp/aiman/releases/latest"

// releaseTagRe matches a published tag. A git-describe version such as
// v0.29.2-3-gabc1234 is a local build with no release behind it. The match also
// keeps a tag safe to place unquoted in a remote script.
var releaseTagRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// InstallPipeAt is the install command for k pinned to release tag: install.sh
// and the binary both come from that release, and install.sh verifies the
// archive against that release's checksums.txt.
//
// The script is read into a variable first. In `curl | sh` a failed download
// feeds sh an empty script and the pipe exits 0, and a cut-off one runs part of
// the installer. The parentheses keep the exit to this command, so the result is
// non-zero whether or not the caller runs under set -e. An invalid tag gives a
// command that fails rather than a script holding the bad value.
func (k Kind) InstallPipeAt(tag string) string {
	if !releaseTagRe.MatchString(tag) {
		return `(echo "aiman: refusing to install: invalid release tag" >&2; exit 1)`
	}
	env := "VERSION=" + tag
	if k != KindServe {
		env += " BINARY_NAME=" + k.Binary()
	}
	return fmt.Sprintf(`(installer="$(curl -sSfL %s%s/install.sh)" || exit 1; printf '%%s\n' "$installer" | %s sh)`,
		releaseDownloadBase, tag, env)
}

// ReleaseTag returns version when it names a published release.
func ReleaseTag(version string) (string, bool) {
	if releaseTagRe.MatchString(version) {
		return version, true
	}
	return "", false
}

// InstallTag is the tag a remote install should use: this client's own release,
// so client and remote match, or the latest release for a local build.
func InstallTag(ctx context.Context, version string) (string, error) {
	if tag, ok := ReleaseTag(version); ok {
		return tag, nil
	}
	return LatestReleaseTag(ctx)
}

// LatestReleaseTag asks GitHub for the newest published release.
func LatestReleaseTag(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "aiman-cli")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("look up latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("look up latest release: GitHub API returned %d", resp.StatusCode)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("look up latest release: %w", err)
	}
	tag, ok := ReleaseTag(release.TagName)
	if !ok {
		return "", fmt.Errorf("look up latest release: unexpected tag %q", release.TagName)
	}
	return tag, nil
}
