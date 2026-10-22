package ui

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usecase"
)

// todoOp is one to-do mutation. Params never includes "as"; Apply adds it.
type todoOp struct {
	Method string
	Params map[string]any
}

func opAdd(session, text, after string) todoOp {
	p := map[string]any{"session": session, "text": text}
	if after != "" {
		p["after"] = after
	}
	return todoOp{Method: "todo.add", Params: p}
}

func opUpdateText(session, id, text string) todoOp {
	return todoOp{Method: "todo.update", Params: map[string]any{
		"session": session,
		"id":      id,
		"text":    text,
	}}
}

func opSetState(session, id string, st domain.TodoState) todoOp {
	return todoOp{Method: "todo.update", Params: map[string]any{
		"session": session,
		"id":      id,
		"state":   string(st),
	}}
}

func opMove(session, id string, toIndex int) todoOp {
	return todoOp{Method: "todo.move", Params: map[string]any{
		"session":  session,
		"id":       id,
		"to_index": toIndex,
	}}
}

func opRemove(session, id string) todoOp {
	return todoOp{Method: "todo.remove", Params: map[string]any{
		"session": session,
		"id":      id,
	}}
}

// todoClient is how the dashboard reads and changes a session's to-do list.
// Reads (Get, Summary) send no author. Apply and Prompt send as "user".
type todoClient interface {
	Get(ctx context.Context, remote config.Remote, sessionID string) (todoSnapshot, error)
	Summary(ctx context.Context, remote config.Remote, sessionIDs []string) (map[string]todoSummary, error)
	Apply(ctx context.Context, remote config.Remote, op todoOp) error
	Prompt(ctx context.Context, remote config.Remote, sessionID, text string) (server.SteerStatus, error)
}

// rpcFunc is one agent-API call. Production uses usecase.ServeRPC; tests
// substitute a recorder so no SSH connection is opened.
type rpcFunc func(ctx context.Context, remote usecase.TerminalExecutor, method string, params any) (json.RawMessage, error)

// sshTodoClient calls the remote agent API. dial builds the SSH executor
// (no connection until the RPC runs); rpc defaults to usecase.ServeRPC.
type sshTodoClient struct {
	dial func(config.Remote) usecase.TerminalExecutor
	rpc  rpcFunc
}

func newSSHTodoClient() *sshTodoClient {
	return &sshTodoClient{dial: dialRemoteSSH, rpc: usecase.ServeRPC}
}

func dialRemoteSSH(r config.Remote) usecase.TerminalExecutor {
	return ssh.NewManager(ssh.Config{Host: r.Host, User: r.User, Root: r.Root})
}

var _ todoClient = (*sshTodoClient)(nil)

func (c *sshTodoClient) call(ctx context.Context, remote config.Remote, method string, params map[string]any) (json.RawMessage, error) {
	raw, err := c.rpc(ctx, c.dial(remote), method, params)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	return raw, nil
}

func (c *sshTodoClient) Get(ctx context.Context, remote config.Remote, sessionID string) (todoSnapshot, error) {
	raw, err := c.call(ctx, remote, "todo.get", map[string]any{"session": sessionID})
	if err != nil {
		return todoSnapshot{}, err
	}
	var body struct {
		List  domain.TodoList    `json:"list"`
		Steer server.SteerStatus `json:"steer"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return todoSnapshot{}, fmt.Errorf("todo.get: %w", err)
	}
	return newTodoSnapshot(body.List, body.Steer), nil
}

func (c *sshTodoClient) Summary(ctx context.Context, remote config.Remote, sessionIDs []string) (map[string]todoSummary, error) {
	params := map[string]any{}
	if sessionIDs != nil {
		params["sessions"] = sessionIDs
	}
	raw, err := c.call(ctx, remote, "todo.summary", params)
	if err != nil {
		return nil, err
	}
	var body struct {
		Sessions map[string]todoSummary `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("todo.summary: %w", err)
	}
	if body.Sessions == nil {
		return map[string]todoSummary{}, nil
	}
	return body.Sessions, nil
}

func (c *sshTodoClient) Apply(ctx context.Context, remote config.Remote, op todoOp) error {
	_, err := c.call(ctx, remote, op.Method, withUser(op.Params))
	return err
}

func (c *sshTodoClient) Prompt(ctx context.Context, remote config.Remote, sessionID, text string) (server.SteerStatus, error) {
	raw, err := c.call(ctx, remote, "steer.send", map[string]any{
		"session": sessionID,
		"text":    text,
		"as":      string(domain.AuthorUser),
		"mode":    "note",
	})
	if err != nil {
		return server.SteerStatus{}, err
	}
	var body struct {
		Steer server.SteerStatus `json:"steer"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return server.SteerStatus{}, fmt.Errorf("steer.send: %w", err)
	}
	return body.Steer, nil
}

// withUser copies params and sets the author. The caller's map is left unchanged.
func withUser(params map[string]any) map[string]any {
	out := make(map[string]any, len(params)+1)
	for k, v := range params {
		out[k] = v
	}
	out["as"] = string(domain.AuthorUser)
	return out
}
