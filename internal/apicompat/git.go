// Package apicompat checks that a release only adds to the surfaces older
// clients and holders depend on: the OpenAPI contract, the gateway's device
// allowlist and the JSON names of the holder files. Each comparison takes the
// previous release's copy and the current one and returns human-readable
// problems; an empty result means compatible.
package apicompat

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
)

// releaseTag is a plain release tag; pre-release and other tags never count
// as the previous release.
var releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

const gitTimeout = 30 * time.Second

// PreviousTag returns the newest release tag by version order that does not
// point at HEAD of the repository in dir, so a tagged release commit compares
// against the release before it. ok is false outside a repository or when no
// such tag exists. git describe is not used: it names the nearest reachable
// tag, not the newest release.
func PreviousTag(dir string) (tag string, ok bool) {
	listed, err := gitOutput(dir, "tag", "--list", "v*.*.*", "--sort=-v:refname")
	if err != nil {
		return "", false
	}
	atHead, err := gitOutput(dir, "tag", "--points-at", "HEAD")
	if err != nil {
		return "", false
	}
	head := strings.Fields(string(atHead))
	for _, t := range strings.Fields(string(listed)) {
		if releaseTag.MatchString(t) && !slices.Contains(head, t) {
			return t, true
		}
	}
	return "", false
}

// ShowAtTag returns path as it was at tag (git show <tag>:<path>).
func ShowAtTag(dir, tag, path string) ([]byte, error) {
	return gitOutput(dir, "show", tag+":"+path)
}

func gitOutput(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) //nolint:gosec // fixed git subcommands; dir, tag and path are the caller's
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
