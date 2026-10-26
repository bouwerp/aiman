package fakeagent

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/bouwerp/aiman/internal/server"
)

// Run sends the script's steps to serve over its socket. It does not need a
// terminal: the steps are the hook and to-do calls a real agent would make.
func Run(socket string, script Script) error {
	for i, step := range script.Steps {
		if step.AfterMS > 0 {
			time.Sleep(time.Duration(step.AfterMS) * time.Millisecond)
		}
		if len(step.Todo) > 0 {
			if err := mirror(socket, script.SessionID, step.Todo); err != nil {
				return fmt.Errorf("step %d mirror: %w", i+1, err)
			}
		}
		if step.Event != "" {
			if err := report(socket, script.SessionID, step); err != nil {
				return fmt.Errorf("step %d report: %w", i+1, err)
			}
		}
		if step.Exit {
			return nil
		}
	}
	return nil
}

func mirror(socket, sessionID string, items []Todo) error {
	rows := make([]map[string]any, 0, len(items))
	for _, it := range items {
		rows = append(rows, map[string]any{"text": it.Text, "status": it.Status})
	}
	return call(socket, "todo.mirror", map[string]any{"session": sessionID, "items": rows})
}

func report(socket, sessionID string, step Step) error {
	params := map[string]any{"id": sessionID, "event": step.Event}
	if step.State != "" {
		params["state"] = step.State
	}
	if step.Message != "" {
		params["message"] = step.Message
	}
	if step.Event == "SessionEnd" {
		params["ended"] = true
	}
	return call(socket, "session.report_agent_session", params)
}

func call(socket, method string, params any) error {
	resp, err := server.Call(socket, method, params)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	return nil
}

// Encode is the script wire form, used by the command and the harness.
func Encode(script Script) ([]byte, error) {
	return json.MarshalIndent(script, "", "  ")
}
