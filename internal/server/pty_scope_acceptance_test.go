//go:build acceptance

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
)

// F5: pty.resize is scoped to the caller's tree like the other pty methods.
func TestF5PTYResizeScoped(t *testing.T) {
	for _, c := range scopeTargetCases(true) {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := scopeServer(t)
			resp := scopeCall(t, srv, c.caller, c.pid, "pty.resize",
				map[string]any{"id": c.target, "cols": 100, "rows": 30})
			refused := resp.Error != nil && resp.Error.Code == CodeSessionScope
			if refused == c.allowed {
				t.Fatalf("refused = %v (error %+v), want allowed = %v", refused, resp.Error, c.allowed)
			}
			if c.caller == "" && c.allowed {
				// An operator call is unchanged: the resize applies.
				got := lifecycleResult(t, resp)
				if got["type"] != "pty_resize" || got["applied"] != true {
					t.Fatalf("operator resize = %v, want applied", got)
				}
			}
		})
	}
}

// attachLines runs pty.attach through handlePTYAttachConn on an in-memory
// connection and returns the first line written and everything after it.
func attachLines(t *testing.T, srv *Server, caller string, pid int, target string) (first, rest string) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"id": target, "cols": 80, "rows": 24})
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(Request{ID: "1", Method: "pty.attach", Params: b, Caller: caller})
	if err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	defer client.Close()
	ctx, cancel := context.WithCancel(withPeerPID(context.Background(), pid, pid != scopeNoPID))
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.handlePTYAttachConn(ctx, server, line)
	}()

	firstCh := make(chan string, 1)
	restCh := make(chan string, 1)
	go func() {
		br := bufio.NewReader(client)
		l, _ := br.ReadString('\n')
		firstCh <- l
		r, _ := io.ReadAll(br)
		restCh <- string(r)
	}()
	first = <-firstCh
	// Whatever the handler would still write after its first line is read
	// before the stream ends.
	cancel()
	<-done
	_ = server.Close()
	rest = <-restCh
	return first, rest
}

func decodeAttachLine(t *testing.T, line string) Response {
	t.Helper()
	var resp Response
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &resp); err != nil {
		t.Fatalf("first line %q: %v", line, err)
	}
	return resp
}

// F5: a session caller may not attach outside its tree. The refusal is the
// only thing written on the connection.
func TestF5PTYAttachRefusedOutsideTree(t *testing.T) {
	srv, pty := scopeServer(t)
	first, rest := attachLines(t, srv, scopeB, scopeNoPID, scopeA)
	resp := decodeAttachLine(t, first)
	if resp.Error == nil || resp.Error.Code != CodeSessionScope {
		t.Fatalf("first line = %q, want a %s error", first, CodeSessionScope)
	}
	if rest != "" {
		t.Fatalf("refused attach wrote more after the error: %q", rest)
	}
	if len(pty.Writes()) != 0 {
		t.Fatalf("refused attach reached the runtime: %v", pty.Writes())
	}
}

// F5: attaching to a session in the caller's tree is unchanged.
func TestF5PTYAttachAllowedInTree(t *testing.T) {
	srv, _ := scopeServer(t)
	first, _ := attachLines(t, srv, scopeA, scopeNoPID, scopeC)
	resp := decodeAttachLine(t, first)
	if resp.Error != nil {
		t.Fatalf("attach in tree refused: %+v", resp.Error)
	}
	if got := lifecycleResult(t, resp); got["type"] != "pty_attached" {
		t.Fatalf("first line = %q, want pty_attached", first)
	}
}
