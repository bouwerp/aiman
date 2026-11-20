package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bouwerp/aiman/internal/console"
	"github.com/bouwerp/aiman/internal/usecase"
)

// SocketRPC calls serve over its unix socket with no caller, as the operator.
// Serve's refusals come back as *usecase.RPCError.
type SocketRPC struct {
	Socket string
}

// Call sends one request; ctx bounds the wait, not serve's own work.
func (s SocketRPC) Call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	if params == nil {
		params = map[string]any{}
	}
	body, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	type answer struct {
		raw json.RawMessage
		err error
	}
	ch := make(chan answer, 1)
	go func() {
		resp, err := serveClient(s.Socket).CallRaw(method, body)
		if err != nil {
			ch <- answer{err: err}
			return
		}
		if resp.Error != nil {
			ch <- answer{err: &usecase.RPCError{Code: resp.Error.Code, Message: resp.Error.Message}}
			return
		}
		raw, err := json.Marshal(resp.Result)
		ch <- answer{raw: raw, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case a := <-ch:
		return a.raw, a.err
	}
}

// AnnounceParams is what the gateway tells serve about its console.
type AnnounceParams struct {
	Base        string
	ConsolePort int
	Scheme      string
	PID         int
}

// AnnounceEvery is the heartbeat; serve treats an announce older than three
// minutes as no gateway.
const AnnounceEvery = time.Minute

// Announce tells serve where the console is (gateway.announce).
func Announce(ctx context.Context, rpc console.RPC, p AnnounceParams) error {
	_, err := rpc.Call(ctx, "gateway.announce", map[string]any{
		"base": p.Base, "console_port": p.ConsolePort, "scheme": p.Scheme, "pid": p.PID,
	})
	if err != nil {
		return fmt.Errorf("gateway.announce: %w", err)
	}
	return nil
}

// RunAnnounce announces at once and every AnnounceEvery until ctx ends,
// logging a failure once per distinct error.
func RunAnnounce(ctx context.Context, rpc console.RPC, p AnnounceParams, logf func(string, ...any)) {
	last := ""
	t := time.NewTicker(AnnounceEvery)
	defer t.Stop()
	for {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := Announce(cctx, rpc, p)
		cancel()
		switch {
		case err != nil && err.Error() != last:
			last = err.Error()
			logf("console announce failed: %v", err)
		case err == nil && last != "":
			last = ""
			logf("console announce works again")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// WatchShareEvents passes serve's harness_share events to cs until ctx ends,
// reconnecting after a dropped stream and polling once after each reconnect
// so an event missed in between is not lost.
func WatchShareEvents(ctx context.Context, socket string, cs *ConsoleShares) {
	backoff := time.Second
	for ctx.Err() == nil {
		stream, err := serveClient(socket).EventsDial()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		stop := context.AfterFunc(ctx, func() { _ = stream.Close() })
		cs.PollOnce(ctx)
		for {
			ev, err := stream.Next()
			if err != nil {
				break
			}
			if ev.Type == "harness_share" {
				cs.ShareEvent(ev.ID, ev.State)
			}
		}
		stop()
		_ = stream.Close()
	}
}
