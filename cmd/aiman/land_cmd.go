package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/bouwerp/aiman/internal/server"
)

// runLand is `aiman land [--session NAME]`: merge this child's HEAD onto its
// coordinator's branch through serve's session.land.
func runLand(args []string, getenv func(string) string) error {
	flags, rest := takeFlags(args)
	if len(rest) != 0 {
		return landUsage()
	}
	session := strings.TrimSpace(flags["session"])
	if session == "" {
		session = strings.TrimSpace(getenv("AIMAN_ID"))
	}
	if session == "" {
		return landUsage()
	}
	raw, err := json.Marshal(map[string]any{"session": session})
	if err != nil {
		return err
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	resp, err := server.CallRaw(sock, "session.land", raw)
	if err != nil {
		code := server.CodeServerNotRunning
		if !errors.Is(err, server.ErrServerNotRunning) {
			code = server.CodeTimeout
		}
		writeCLIError(code, err.Error())
		return err
	}
	if resp.Error != nil {
		writeCLIError(resp.Error.Code, resp.Error.Message)
		return errors.New(resp.Error.Message)
	}
	return writeJSON(resp.Result)
}

func landUsage() error {
	fmt.Fprintf(os.Stderr, "Usage: aiman land [--session NAME]\n")
	fmt.Fprintf(os.Stderr, "--session defaults to $AIMAN_ID; run it from a child session after committing and writing .aiman_report.md.\n")
	return errUsage
}
