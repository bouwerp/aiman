package server

import (
	"encoding/json"
	"fmt"
)

const ProtocolVersion = 1

const (
	CodeInvalidParams    = "invalid_params"
	CodeNotFound         = "not_found"
	CodeNameTaken        = "name_taken"
	CodeServerNotRunning = "server_not_running"
	CodeAlreadyRunning   = "already_running"
	CodeAgentBlocked     = "agent_blocked"
	CodeTimeout          = "timeout"
	CodeCreateFailed     = "create_failed"
	CodeJiraUnavailable  = "jira_unavailable"
	CodeProtocolMismatch = "protocol_mismatch"
)

type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type Response struct {
	ID     string `json:"id"`
	Result any    `json:"result,omitempty"`
	Error  *Error `json:"error,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func EncodeResponse(resp Response) ([]byte, error) {
	b, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
