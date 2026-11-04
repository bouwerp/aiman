package gateway

import (
	"context"
	"fmt"
	"path/filepath"
)

// InstanceLockName is the flock the running gateway holds in the aiman dir.
const InstanceLockName = "gateway.lock"

// LockInstance holds the host's one gateway slot until the release is called
// or the process exits. Two gateways on one tsnet state fight over the node
// key and send every push twice, and concurrent starts can launch two. It
// waits until ctx ends, so a restart outlasts the old process's shutdown.
func LockInstance(ctx context.Context, dir string) (func(), error) {
	release, err := lockFile(ctx, filepath.Join(dir, InstanceLockName))
	if err != nil {
		return nil, fmt.Errorf("another aiman-gateway is running: %w", err)
	}
	return release, nil
}
