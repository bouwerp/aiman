//go:build windows

package main

import "errors"

func execSelf(string, []string, []string) error {
	return errors.New("exec is not supported on windows")
}
