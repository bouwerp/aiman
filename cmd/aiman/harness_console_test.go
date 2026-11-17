package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/usecase"
)

type fakeConsoleRPC struct {
	mu       sync.Mutex
	statuses []string
	verdicts []map[string]any
	status   []map[string]any
}

func (f *fakeConsoleRPC) Status(_ context.Context, p map[string]any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = append(f.status, p)
	s := f.statuses[0]
	if len(f.statuses) > 1 {
		f.statuses = f.statuses[1:]
	}
	return json.RawMessage(s), nil
}

func (f *fakeConsoleRPC) Verdict(_ context.Context, p map[string]any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verdicts = append(f.verdicts, p)
	return json.RawMessage(`{"type":"harness_verdict","verdict":{"run":"r1","step":1,"result":"fail"},"todo":"t1"}`), nil
}

type consoleProc struct {
	done chan struct{}
	once sync.Once
}

func (p *consoleProc) Done() <-chan struct{} { return p.done }
func (p *consoleProc) Err() error            { return nil }
func (p *consoleProc) Stop() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

const consoleRun = `{"type":"harness_run","run":{"id":"r1","session":"s1","scenario":"refund","sha":"a1b2c3d4e5f6","source":"file","state":"%s",
"ports":[{"name":"web","service":"app","host_port":41873},{"name":"api","service":"app","host_port":41874}],
"steps":[{"n":1,"do":"Open {{web}}/orders/42 and press Refund.","expect":"Refunded"},{"n":2,"do":"Call {{api}}/health"}],"checks":[]}}`

func TestHarnessConsoleForwards(t *testing.T) {
	rpc := &fakeConsoleRPC{statuses: []string{
		strings.Replace(consoleRun, "%s", "starting", 1),
		strings.Replace(consoleRun, "%s", "ready", 1),
	}}
	var argvs [][]string
	var procs []*consoleProc
	next := 52011
	fwd := usecase.HarnessForwarder{
		Target: "code@regent0",
		Start: func(_ context.Context, args []string) (usecase.ForwardProc, error) {
			argvs = append(argvs, args)
			p := &consoleProc{done: make(chan struct{})}
			procs = append(procs, p)
			return p, nil
		},
		FreePort: func() (int, error) { next++; return next, nil },
		Ready:    func(context.Context, int, usecase.ForwardProc) error { return nil },
	}
	// step 1 fail with a note, step 2 left, scenario skipped, then quit.
	in := strings.NewReader("f\nUI says Refunded but status stays paid\n\n\nq\n")
	var out bytes.Buffer
	err := harnessConsole(context.Background(), harnessArgs{session: "s1"}, consoleDeps{
		rpc: rpc, forwarder: fwd, in: in, out: &out, sleep: func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("console: %v\n%s", err, out.String())
	}
	if rpc.status[0]["session"] != "s1" || rpc.status[1]["run"] != "r1" {
		t.Fatalf("status calls %v", rpc.status)
	}
	if len(argvs) != 2 {
		t.Fatalf("started %d ssh children, want one per relay", len(argvs))
	}
	for i, want := range []string{"127.0.0.1:52012:127.0.0.1:41873", "127.0.0.1:52013:127.0.0.1:41874"} {
		if !strings.Contains(strings.Join(argvs[i], " "), "-L "+want) {
			t.Errorf("argv %d = %v, want -L %s", i, argvs[i], want)
		}
	}
	text := out.String()
	for _, want := range []string{
		"web", "http://127.0.0.1:52012", "41873",
		"Open http://127.0.0.1:52012/orders/42 and press Refund.",
		"Call http://127.0.0.1:52013/health",
		"t1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if len(rpc.verdicts) != 1 {
		t.Fatalf("verdicts %v", rpc.verdicts)
	}
	v := rpc.verdicts[0]
	if v["run"] != "r1" || v["step"] != 1 || v["result"] != "fail" || v["note"] != "UI says Refunded but status stays paid" {
		t.Fatalf("verdict params %v", v)
	}
	for i, p := range procs {
		select {
		case <-p.done:
		default:
			t.Errorf("forward %d left running after the console ended", i)
		}
	}
}

func TestHarnessConsoleStopsOnCancel(t *testing.T) {
	rpc := &fakeConsoleRPC{statuses: []string{strings.Replace(consoleRun, "%s", "ready", 1)}}
	var procs []*consoleProc
	fwd := usecase.HarnessForwarder{
		Target: "h",
		Start: func(context.Context, []string) (usecase.ForwardProc, error) {
			p := &consoleProc{done: make(chan struct{})}
			procs = append(procs, p)
			return p, nil
		},
		FreePort: func() (int, error) { return 50000 + len(procs), nil },
		Ready:    func(context.Context, int, usecase.ForwardProc) error { return nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	done := make(chan error, 1)
	var out syncBuffer
	go func() {
		done <- harnessConsole(ctx, harnessArgs{run: "r1"}, consoleDeps{rpc: rpc, forwarder: fwd, in: pr, out: &out, sleep: func(time.Duration) {}})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "step 1") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel() // ctrl+c
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("console did not stop on cancel")
	}
	for i, p := range procs {
		select {
		case <-p.done:
		default:
			t.Errorf("forward %d left running after ctrl+c", i)
		}
	}
	if len(rpc.verdicts) != 0 {
		t.Fatalf("verdicts sent: %v", rpc.verdicts)
	}
}

func TestHarnessConsoleRefusesStoppedRun(t *testing.T) {
	rpc := &fakeConsoleRPC{statuses: []string{strings.Replace(consoleRun, "%s", "stopped", 1)}}
	fwd := usecase.HarnessForwarder{Start: func(context.Context, []string) (usecase.ForwardProc, error) {
		t.Fatal("forwarded a stopped run")
		return nil, nil
	}}
	var out bytes.Buffer
	err := harnessConsole(context.Background(), harnessArgs{session: "s1"}, consoleDeps{rpc: rpc, forwarder: fwd, in: strings.NewReader(""), out: &out, sleep: func(time.Duration) {}})
	if err == nil {
		t.Fatal("console accepted a stopped run")
	}
}

func TestHarnessConsoleRemotePick(t *testing.T) {
	remotes := []config.Remote{{Name: "regent0", Host: "h0"}, {Name: "Box", Host: "h1"}}
	if r, err := pickConsoleRemote(remotes, "", "box"); err != nil || r.Host != "h1" {
		t.Fatalf("by name: %v %v", r, err)
	}
	if r, err := pickConsoleRemote(remotes, "regent0", ""); err != nil || r.Host != "h0" {
		t.Fatalf("active: %v %v", r, err)
	}
	if _, err := pickConsoleRemote(remotes, "", ""); err == nil {
		t.Fatal("two remotes and no --remote picked one")
	}
	if _, err := pickConsoleRemote(remotes, "", "nope"); err == nil {
		t.Fatal("unknown remote accepted")
	}
	if r, err := pickConsoleRemote(remotes[:1], "", ""); err != nil || r.Host != "h0" {
		t.Fatalf("single: %v %v", r, err)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
