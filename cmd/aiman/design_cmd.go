package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/server"
)

func runDesign(args []string, getenv func(string) string) error {
	if len(args) == 0 || args[0] != "publish" {
		return designUsage()
	}
	flags, rest := takeFlags(args[1:])
	if len(rest) != 1 || rest[0] == "" {
		return designUsage()
	}
	session := strings.TrimSpace(flags["session"])
	if session == "" {
		session = strings.TrimSpace(getenv("AIMAN_ID"))
	}
	if session == "" {
		return designUsage()
	}
	return callDesign(designParams(session, rest[0], flags["key"]))
}

func designUsage() error {
	fmt.Fprintf(os.Stderr, "Usage: aiman design publish <file> [--key KEY] [--session NAME]\n")
	fmt.Fprintf(os.Stderr, "--session defaults to $AIMAN_ID; one of them is required.\n")
	return errUsage
}

// designParams maps the CLI arguments onto design.publish. A relative file is
// made absolute from the caller's directory, which is the session's.
func designParams(session, file, key string) map[string]any {
	if !filepath.IsAbs(file) {
		if wd, err := os.Getwd(); err == nil {
			file = filepath.Join(wd, file)
		}
	}
	params := map[string]any{"session": session, "file": file}
	if key = strings.TrimSpace(key); key != "" {
		params["key"] = key
	}
	return params
}

func callDesign(params map[string]any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	resp, err := server.CallRaw(sock, "design.publish", raw)
	if err != nil {
		code := server.CodeServerNotRunning
		if !errors.Is(err, server.ErrServerNotRunning) {
			code = server.CodePublishFailed
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
