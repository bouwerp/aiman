//go:build windows

package main

import "errors"

// signalServe has nothing to send on Windows, which has no SIGUSR1.
func signalServe(int) error {
	return errors.New("aiman host dump needs SIGUSR1, which Windows does not have; serve writes a dump on its own when requests sit at the cap")
}
