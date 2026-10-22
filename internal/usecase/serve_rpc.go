package usecase

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrRPCUnsupported means the remote's aiman binary or serve is older than the
// requested method.
var ErrRPCUnsupported = errors.New("remote aiman does not support this method; update it from the Agent API page")

// ErrServeNotRunning means `aiman serve` on the remote has no listening socket.
var ErrServeNotRunning = errors.New("aiman serve is not running on the remote")

// RPCError is an error answer from serve.
type RPCError struct {
	Code    string
	Message string
}

func (e *RPCError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// rpcUnsupportedMarkers appear in output when the remote cannot route the
// method: serve does not know it, the binary has no `rpc` command, or no
// binary is installed.
var rpcUnsupportedMarkers = []string{"unknown method", "unknown command", "command not found"}

// rpcAnswer mirrors server.Response; usecase must not import internal/server.
type rpcAnswer struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// ServeRPC calls one agent API method on the remote in a single SSH round trip.
// The params travel as base64 inside single quotes, so no shell ever parses
// user text.
func ServeRPC(ctx context.Context, remote TerminalExecutor, method string, params any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encode %s params: %w", method, err)
	}
	enc := base64.StdEncoding.EncodeToString(raw)
	out, execErr := remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf(
		"printf %%s '%s' | base64 -d | aiman rpc %s --params-stdin", enc, method))

	// A non-zero `aiman rpc` exit still prints the {"error":…} answer line.
	if ans, ok := lastRPCAnswer(out); ok {
		if ans.Error == nil {
			return ans.Result, nil
		}
		switch {
		case ans.Error.Code == "server_not_running":
			return nil, ErrServeNotRunning
		case rpcLooksUnsupported(ans.Error.Message):
			return nil, ErrRPCUnsupported
		}
		return nil, &RPCError{Code: ans.Error.Code, Message: ans.Error.Message}
	}

	detail := out
	if execErr != nil {
		detail += " " + execErr.Error()
	}
	if rpcLooksUnsupported(detail) {
		return nil, ErrRPCUnsupported
	}
	if execErr != nil {
		return nil, fmt.Errorf("rpc %s: %w", method, execErr)
	}
	return nil, fmt.Errorf("rpc %s: no answer from remote: %s", method, strings.TrimSpace(out))
}

func rpcLooksUnsupported(s string) bool {
	for _, m := range rpcUnsupportedMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// lastRPCAnswer returns the last output line that parses as a response object.
func lastRPCAnswer(out string) (rpcAnswer, bool) {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ans rpcAnswer
		if err := json.Unmarshal([]byte(line), &ans); err != nil {
			continue
		}
		if ans.Error == nil && ans.Result == nil {
			continue
		}
		return ans, true
	}
	return rpcAnswer{}, false
}
