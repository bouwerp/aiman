//go:build unix

package main

import "syscall"

// signalServe sends SIGUSR1 to serve, which writes a dump.
func signalServe(pid int) error {
	return syscall.Kill(pid, syscall.SIGUSR1)
}
