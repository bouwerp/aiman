//go:build pileup_c

package main

// Contract (docs/specs/client-pileup-guard.md, Behaviour P1, remote side):
//
//   - func runRPCStream(stdin io.Reader, stdout io.Writer) error
//     implements `aiman rpc --stream`; runRPC(args, …) calls it when args is
//     exactly ["--stream"], with os.Stdout. It resolves serve's socket with
//     socketPath() (so AIMAN_SOCKET_PATH applies), writes the hello line
//     `{"stream":1}` and flushes it before it reads stdin, then reads one
//     `{"id","method","params"}` line at a time. Each listed method runs in its
//     own goroutine through server.CallRaw (one socket dial per call); each
//     answer is one line `{"id":<request id>,"result":…}` or
//     `{"id":<request id>,"error":{"code","message"}}`, written under a mutex,
//     in completion order. A method not in rpcMethods answers an error with code
//     invalid_params and never dials. At most 8 calls are in flight; a ninth
//     answers code server_busy at once. EOF on stdin returns nil.

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/server"
)

// streamServe is a fake serve socket: each connection carries one request,
// answered on its own goroutine by answer. It counts dials.
type streamServe struct {
	dials atomic.Int32
}

func newStreamServe(t *testing.T, answer func(req server.Request) server.Response) *streamServe {
	t.Helper()
	dir, err := os.MkdirTemp("", "rps")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := &streamServe{}
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			s.dials.Add(1)
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				line, rerr := bufio.NewReader(c).ReadBytes('\n')
				if rerr != nil {
					return
				}
				var req server.Request
				if json.Unmarshal(line, &req) != nil {
					return
				}
				resp := answer(req)
				resp.ID = req.ID
				b, _ := server.EncodeResponse(resp)
				_, _ = c.Write(b)
			}(c)
		}
	}()
	t.Setenv("AIMAN_SOCKET_PATH", sock)
	t.Setenv("AIMAN_ID", "")
	return s
}

// streamRun runs runRPCStream on pipes and collects its stdout lines.
type streamRun struct {
	in    *io.PipeWriter
	lines chan string
	done  chan error
}

func startStream(t *testing.T) *streamRun {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	r := &streamRun{in: inW, lines: make(chan string, 64), done: make(chan error, 1)}
	go func() {
		err := runRPCStream(inR, outW)
		_ = outW.Close()
		r.done <- err
	}()
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			r.lines <- sc.Text()
		}
		close(r.lines)
	}()
	t.Cleanup(func() { _ = inW.Close() })
	return r
}

func (r *streamRun) next(t *testing.T) string {
	t.Helper()
	select {
	case l, ok := <-r.lines:
		if !ok {
			t.Fatal("stream stdout closed")
		}
		return l
	case <-time.After(3 * time.Second):
		t.Fatal("no line from the stream within 3s")
	}
	return ""
}

func (r *streamRun) send(t *testing.T, id, method, params string) {
	t.Helper()
	line := `{"id":"` + id + `","method":"` + method + `","params":` + params + "}\n"
	if _, err := r.in.Write([]byte(line)); err != nil {
		t.Fatalf("write request %s: %v", id, err)
	}
}

type streamAnswer struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func parseAnswer(t *testing.T, line string) streamAnswer {
	t.Helper()
	var a streamAnswer
	if err := json.Unmarshal([]byte(line), &a); err != nil {
		t.Fatalf("answer %q is not JSON: %v", line, err)
	}
	if a.ID == "" {
		t.Fatalf("answer %q has no id", line)
	}
	return a
}

func okAnswer(req server.Request) server.Response {
	return server.Response{Result: map[string]any{"method": req.Method}}
}

func TestAcceptanceRPCStreamHelloBeforeAnyRequest(t *testing.T) {
	srv := newStreamServe(t, okAnswer)
	r := startStream(t)
	// Nothing has been written to stdin: the hello must already be flushed.
	hello := r.next(t)
	var h map[string]any
	if err := json.Unmarshal([]byte(hello), &h); err != nil || h["stream"] != float64(1) || len(h) != 1 {
		t.Fatalf("first line = %q, want {\"stream\":1}", hello)
	}
	if n := srv.dials.Load(); n != 0 {
		t.Fatalf("dials before any request = %d", n)
	}
}

func TestAcceptanceRPCStreamAnswersByIDOutOfOrder(t *testing.T) {
	release := make(chan struct{})
	newStreamServe(t, func(req server.Request) server.Response {
		if strings.Contains(string(req.Params), `"slow"`) {
			<-release
		}
		return server.Response{Result: map[string]any{"params": json.RawMessage(req.Params)}}
	})
	r := startStream(t)
	_ = r.next(t) // hello
	r.send(t, "1", "todo.get", `{"session":"slow"}`)
	r.send(t, "2", "todo.get", `{"session":"fast"}`)

	first := parseAnswer(t, r.next(t))
	if first.ID != "2" || first.Error != nil || !strings.Contains(string(first.Result), "fast") {
		t.Fatalf("first answer = %+v, want id 2 with the fast result", first)
	}
	close(release)
	second := parseAnswer(t, r.next(t))
	if second.ID != "1" || second.Error != nil || !strings.Contains(string(second.Result), "slow") {
		t.Fatalf("second answer = %+v, want id 1 with the slow result", second)
	}
}

func TestAcceptanceRPCStreamRefusesUnlistedMethodWithoutDial(t *testing.T) {
	srv := newStreamServe(t, okAnswer)
	r := startStream(t)
	_ = r.next(t)
	r.send(t, "9", "todo.mirror", `{"session":"s1"}`)
	a := parseAnswer(t, r.next(t))
	if a.ID != "9" || a.Error == nil || a.Error.Code != server.CodeInvalidParams {
		t.Fatalf("answer = %+v, want id 9 refused invalid_params", a)
	}
	if n := srv.dials.Load(); n != 0 {
		t.Fatalf("an unlisted method dialled serve %d times", n)
	}
	// The stream keeps serving after a refusal.
	r.send(t, "10", "todo.get", `{"session":"s1"}`)
	if b := parseAnswer(t, r.next(t)); b.ID != "10" || b.Error != nil {
		t.Fatalf("answer after refusal = %+v", b)
	}
}

func TestAcceptanceRPCStreamNinthInFlightIsServerBusy(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	srv := newStreamServe(t, func(req server.Request) server.Response {
		<-release
		return okAnswer(req)
	})
	r := startStream(t)
	_ = r.next(t)
	for i := 1; i <= 8; i++ {
		r.send(t, "h"+string(rune('0'+i)), "todo.get", `{"session":"s1"}`)
	}
	deadline := time.Now().Add(3 * time.Second)
	for srv.dials.Load() < 8 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of 8 calls reached serve", srv.dials.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.send(t, "ninth", "todo.get", `{"session":"s1"}`)
	busy := parseAnswer(t, r.next(t))
	if busy.ID != "ninth" || busy.Error == nil || busy.Error.Code != server.CodeServerBusy {
		t.Fatalf("ninth answer = %+v, want server_busy at once", busy)
	}
	if n := srv.dials.Load(); n != 8 {
		t.Fatalf("dials = %d, the ninth call must not reach serve", n)
	}
	once.Do(func() { close(release) })
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		a := parseAnswer(t, r.next(t))
		if a.Error != nil {
			t.Fatalf("held call %s failed: %+v", a.ID, a.Error)
		}
		seen[a.ID] = true
	}
	if len(seen) != 8 {
		t.Fatalf("held calls answered = %v, want 8 distinct ids", seen)
	}
}

func TestAcceptanceRPCStreamEOFEndsProcess(t *testing.T) {
	newStreamServe(t, okAnswer)
	r := startStream(t)
	_ = r.next(t)
	r.send(t, "1", "todo.get", `{"session":"s1"}`)
	_ = parseAnswer(t, r.next(t))
	_ = r.in.Close()
	select {
	case err := <-r.done:
		if err != nil {
			t.Fatalf("runRPCStream after EOF = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runRPCStream did not return after EOF on stdin")
	}
}
