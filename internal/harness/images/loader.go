package images

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// ErrImageGone is an approved image the engine no longer holds.
var ErrImageGone = errors.New("image not in the engine")

// Loader puts a quarantined image into the Docker engine.
type Loader interface {
	// Load loads the OCI layout at dir and returns the engine's ID of the
	// image named LocalName(digest).
	Load(ctx context.Context, dir, digest string) (string, error)
	// Present returns the engine's ID of ref, or ErrImageGone.
	Present(ctx context.Context, ref string) (string, error)
	// Remove deletes an image the engine loaded.
	Remove(ctx context.Context, ref string) error
}

// DockerLoader runs docker load, image inspect and rmi.
type DockerLoader struct{}

// Load streams the layout as a tar into docker load: the engine reads the
// OCI layout the vetting checked, with no network.
func (DockerLoader) Load(ctx context.Context, dir, digest string) (string, error) {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(tarTo(dir, pw)) }()
	cmd := exec.CommandContext(ctx, "docker", "load", "--quiet")
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = pr
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	_ = pr.Close()
	if err != nil {
		return "", fmt.Errorf("docker load: %w: %s", err, strings.TrimSpace(out.String()))
	}
	id, err := DockerLoader{}.Present(ctx, LocalName(digest))
	if err != nil {
		return "", fmt.Errorf("after docker load (%s): %w", strings.TrimSpace(out.String()), err)
	}
	return id, nil
}

// Present inspects ref.
func (DockerLoader) Present(ctx context.Context, ref string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", ref) //nolint:gosec // G204: an image ID or aiman's local name
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "No such image") {
			return "", ErrImageGone
		}
		return "", fmt.Errorf("docker image inspect %s: %w: %s", ref, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Remove runs docker rmi.
func (DockerLoader) Remove(ctx context.Context, ref string) error {
	out, err := exec.CommandContext(ctx, "docker", "rmi", ref).CombinedOutput() //nolint:gosec // G204: an image ID or aiman's local name
	if err != nil {
		return fmt.Errorf("docker rmi %s: %w: %s", ref, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// IDMatches reports whether a loaded image's ID is the scanned image: the
// platform manifest digest (containerd image store) or the config digest
// (classic store). Both name exactly the bytes the scan read.
func IDMatches(id string, rec Record) bool {
	return id != "" && (id == rec.Digest || id == rec.ConfigDigest)
}
