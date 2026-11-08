package main

import (
	"fmt"
	"os"

	"github.com/bouwerp/aiman/internal/server"
)

// runHost is `aiman host stats|version|update`.
func runHost(args []string) error {
	if len(args) > 0 && (args[0] == "version" || args[0] == "update") {
		return runHostUpdateCmd(args, os.Stdout)
	}
	if len(args) != 1 || args[0] != "stats" {
		fmt.Fprint(os.Stderr, hostUsage)
		return errUsage
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	return callAndPrint(sock, "host.stats", nil)
}
