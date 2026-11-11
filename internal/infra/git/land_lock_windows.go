//go:build windows

package git

import (
	"context"
	"errors"
)

// lockLand refuses: land runs in serve, which runs on Unix hosts. This file
// only keeps the package building elsewhere.
func lockLand(context.Context, string) (func(), error) {
	return nil, errors.New("land needs a Unix host")
}
