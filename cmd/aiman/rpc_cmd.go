package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/bouwerp/aiman/internal/server"
)

// rpcMethods is the one allowlist of agent API methods the laptop may call
// through `aiman rpc`. Methods that only the host's own agents may call (for
// example todo.mirror) must never be added.
var rpcMethods = map[string]bool{
	"session.register":    true,
	"session.log":         true,
	"todo.get":            true,
	"todo.summary":        true,
	"todo.add":            true,
	"todo.update":         true,
	"todo.move":           true,
	"todo.remove":         true,
	"todo.ensure_done":    true,
	"todo.gate":           true,
	"todo.approve_design": true,
	"todo.approve_review": true,
	"steer.send":          true,
	"handover.write":      true,
	"handover.point":      true,
	"usage.get":           true,
	"context.put":         true,
	"context.verify":      true,
	"memory.jobs":         true,
	"memory.done":         true,
	"ci.get":              true,
	"ci.watch":            true,
	"verify.start":        true,
	"verify.status":       true,
}

// rpcParams validates the method and params. It passes the bytes through
// untouched: authorship ("as") is decided by the server, not here.
func rpcParams(method string, raw []byte) ([]byte, error) {
	if !rpcMethods[method] {
		return nil, fmt.Errorf("unknown method %q", method)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, errors.New("params must be a JSON object")
	}
	return raw, nil
}

// runRPC implements `aiman rpc METHOD --params-stdin`. It prints the whole
// server response as one compact JSON line and fails when the answer is an
// error.
func runRPC(args []string, stdin io.Reader, getenv func(string) string) error {
	_ = getenv
	if len(args) != 2 || args[1] != "--params-stdin" {
		writeCLIError(server.CodeInvalidParams, "usage: aiman rpc METHOD --params-stdin")
		return errUsage
	}
	method := args[0]
	raw, err := io.ReadAll(stdin)
	if err != nil {
		writeCLIError(server.CodeInvalidParams, err.Error())
		return err
	}
	params, err := rpcParams(method, raw)
	if err != nil {
		writeCLIError(server.CodeInvalidParams, err.Error())
		return err
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	resp, err := server.CallRaw(sock, method, params)
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	line, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", line)
	if resp.Error != nil {
		return errors.New(resp.Error.Message)
	}
	return nil
}
