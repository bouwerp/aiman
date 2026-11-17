package edge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Client is serve's side of the control socket. Each call dials, sends one
// request with the token and reads one reply.
type Client struct {
	Socket string
	Token  string
}

// Add starts a relay and returns its status.
func (c Client) Add(ctx context.Context, spec RelaySpec) (RelayStatus, error) {
	resp, err := c.call(ctx, Request{Op: OpAdd, Relay: &spec})
	if err != nil {
		return RelayStatus{}, err
	}
	if resp.Relay == nil {
		return RelayStatus{}, fmt.Errorf("edge add: reply has no relay")
	}
	return *resp.Relay, nil
}

// Remove stops a relay.
func (c Client) Remove(ctx context.Context, name string) error {
	_, err := c.call(ctx, Request{Op: OpRemove, Name: name})
	return err
}

// List returns every relay.
func (c Client) List(ctx context.Context) ([]RelayStatus, error) {
	resp, err := c.call(ctx, Request{Op: OpList})
	if err != nil {
		return nil, err
	}
	return resp.Relays, nil
}

// Health returns the edge's health.
func (c Client) Health(ctx context.Context) (Health, error) {
	resp, err := c.call(ctx, Request{Op: OpHealth})
	if err != nil {
		return Health{}, err
	}
	if resp.Health == nil {
		return Health{}, fmt.Errorf("edge health: reply has no health")
	}
	return *resp.Health, nil
}

// call returns an *Error for a refusal from the edge and a wrapped error for
// a transport failure.
func (c Client) call(ctx context.Context, req Request) (Response, error) {
	req.Token = c.Token
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return Response{}, fmt.Errorf("dial edge control: %w", err)
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(10 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, fmt.Errorf("send edge %s: %w", req.Op, err)
	}
	line, err := bufio.NewReaderSize(conn, 4096).ReadBytes('\n')
	if err != nil {
		return Response{}, fmt.Errorf("read edge %s reply: %w", req.Op, err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, fmt.Errorf("decode edge %s reply: %w", req.Op, err)
	}
	if resp.Error != nil {
		return Response{}, resp.Error
	}
	if !resp.OK {
		return Response{}, fmt.Errorf("edge %s: not ok", req.Op)
	}
	return resp, nil
}
