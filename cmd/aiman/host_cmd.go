package main

import (
	"fmt"
	"os"

	"github.com/bouwerp/aiman/internal/server"
)

// runHost is `aiman host stats`, the host.stats call.
func runHost(args []string) error {
	if len(args) != 1 || args[0] != "stats" {
		fmt.Fprintf(os.Stderr, "Usage: aiman host stats\n")
		return errUsage
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	return callAndPrint(sock, "host.stats", nil)
}
