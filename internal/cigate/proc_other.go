//go:build !unix

package cigate

import "os/exec"

func setProcessGroup(*exec.Cmd) {}
