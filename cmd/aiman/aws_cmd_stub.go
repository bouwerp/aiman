//go:build acceptance

package main

import (
	"errors"
	"io"

	"github.com/bouwerp/aiman/internal/infra/config"
)

// runAWS is a compile stub for the acceptance tests; delete this file when
// aws_cmd.go implements the command.
func runAWS(args []string, cfg *config.Config, out io.Writer) error {
	return errors.New("aiman aws: not implemented")
}
