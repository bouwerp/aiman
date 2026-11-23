//go:build !windows

package main

import "syscall"

// #nosec G702 -- path is this binary's own resolved path.
func execSelf(path string, argv, env []string) error { return syscall.Exec(path, argv, env) }
