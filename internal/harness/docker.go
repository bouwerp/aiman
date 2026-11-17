package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/harness/edge"
)

// EdgeImage is the edge's base image: busybox 1.36, pinned by its Docker Hub
// index digest. The edge runs aiman's own static binary, mounted read-only,
// so the image only has to give it a root filesystem; busybox is small and
// carries a shell for debugging the container. The image vetter fetches,
// scans and loads it as a builtin image the first time a run needs it.
const EdgeImage = "busybox:1.36@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662"

// ErrImageNotFound is Docker's answer for an image that is not on the host.
var ErrImageNotFound = errors.New("image not on the host")

// ComposeCall is one docker compose command against a run's project. Dir
// holds compose.yaml; Stdin, when set, is the command's input.
type ComposeCall struct {
	Dir     string
	Project string
	Args    []string
	Stdin   io.Reader
}

// Docker is what the manager needs from the host's Docker engine. Compose
// returns the command's combined output and exit code; err is set only when
// the command could not run at all.
type Docker interface {
	ImageID(ctx context.Context, ref string) (string, error)
	Compose(ctx context.Context, call ComposeCall) (out string, exit int, err error)
}

// EdgeControl reads a run's edge over its control socket.
type EdgeControl interface {
	Health(ctx context.Context, socket, token string) (edge.Health, error)
	// DAPDrop closes the client holding the DAP relay and returns it.
	DAPDrop(ctx context.Context, socket, token, relay string) (*edge.DAPHolder, error)
}

// ExecDocker runs the docker CLI.
type ExecDocker struct{}

// ImageID returns the local image ID of ref, or ErrImageNotFound.
func (ExecDocker) ImageID(ctx context.Context, ref string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", ref) //nolint:gosec // G204: ref is a validated image reference passed as one argument
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "No such image") {
			return "", ErrImageNotFound
		}
		return "", fmt.Errorf("docker image inspect %s: %w: %s", ref, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Compose runs docker compose -p <project> [-f <dir>/compose.yaml] args. The
// file is left out when it is gone, so a stale project can still be taken
// down by its name.
func (ExecDocker) Compose(ctx context.Context, call ComposeCall) (string, int, error) {
	args := []string{"compose", "-p", call.Project}
	file := filepath.Join(call.Dir, "compose.yaml")
	if _, err := os.Stat(file); err == nil {
		args = append(args, "-f", file)
	}
	args = append(args, call.Args...)
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // G204: aiman's own compose arguments
	if _, err := os.Stat(call.Dir); err == nil {
		cmd.Dir = call.Dir
	}
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = call.Stdin
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.String(), 0, nil
	case errors.As(err, &ee) && ee.ExitCode() > 0:
		return out.String(), ee.ExitCode(), nil
	default:
		return out.String(), -1, fmt.Errorf("docker compose %s: %w", strings.Join(call.Args, " "), err)
	}
}

// SocketEdge waits for the edge's control socket, then asks the edge for its
// health with the run's token.
type SocketEdge struct {
	// Wait bounds the wait for the socket; zero is 20s.
	Wait time.Duration
}

// Health polls for socket, then calls the edge.
func (e SocketEdge) Health(ctx context.Context, socket, token string) (edge.Health, error) {
	wait := e.Wait
	if wait <= 0 {
		wait = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return edge.Health{}, fmt.Errorf("the edge made no control socket within %s", wait)
		case <-time.After(200 * time.Millisecond):
		}
	}
	return edge.Client{Socket: socket, Token: token}.Health(ctx)
}

// DAPDrop asks the edge to close the DAP relay's holder.
func (SocketEdge) DAPDrop(ctx context.Context, socket, token, relay string) (*edge.DAPHolder, error) {
	return edge.Client{Socket: socket, Token: token}.DAPDrop(ctx, relay)
}
