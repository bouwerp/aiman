package usecase

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

type fakeForwardProc struct {
	done    chan struct{}
	err     error
	stopped bool
	mu      sync.Mutex
}

func newFakeForwardProc() *fakeForwardProc { return &fakeForwardProc{done: make(chan struct{})} }

func (p *fakeForwardProc) Done() <-chan struct{} { return p.done }
func (p *fakeForwardProc) Err() error            { return p.err }

func (p *fakeForwardProc) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.stopped {
		p.stopped = true
		close(p.done)
	}
	return nil
}

func (p *fakeForwardProc) exit(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
	p.stopped = true
	close(p.done)
}

func argValue(args []string, flag, prefix string) []string {
	var out []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && strings.HasPrefix(args[i+1], prefix) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func TestHarnessForwardNoBackground(t *testing.T) {
	args := HarnessForwardArgs("code@regent0", 52012, 41873)
	if slices.Contains(args, "-f") {
		t.Fatalf("argv backgrounds ssh: %v", args)
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && len(a) > 2 && !strings.Contains(a, "=") && strings.ContainsRune(a[1:], 'f') {
			t.Fatalf("argv has a combined flag with -f: %q", a)
		}
	}
	if !slices.Contains(args, "-N") {
		t.Fatalf("argv lacks -N: %v", args)
	}
	opts := strings.Join(argValue(args, "-o", ""), " ")
	for _, want := range []string{"ExitOnForwardFailure=yes", "ForwardAgent=no", "BatchMode=yes", "ControlPath=none", "ControlMaster=no"} {
		if !strings.Contains(opts, want) {
			t.Errorf("argv lacks -o %s: %v", want, args)
		}
	}
	fwd := argValue(args, "-L", "")
	if len(fwd) != 1 || fwd[0] != "127.0.0.1:52012:127.0.0.1:41873" {
		t.Fatalf("-L = %v, want one forward binding 127.0.0.1 on both ends", fwd)
	}
	if args[len(args)-1] != "code@regent0" {
		t.Fatalf("target is not last: %v", args)
	}
	// Option values must not be readable as a remote command.
	if slices.Contains(args[:len(args)-1], "code@regent0") {
		t.Fatalf("target appears before the end: %v", args)
	}
}

func TestHarnessForwardOpenAndClose(t *testing.T) {
	var started [][]string
	var procs []*fakeForwardProc
	ports := []int{50001, 50002}
	f := HarnessForwarder{
		Target: "code@h",
		Start: func(_ context.Context, args []string) (ForwardProc, error) {
			started = append(started, args)
			p := newFakeForwardProc()
			procs = append(procs, p)
			return p, nil
		},
		FreePort: func() (int, error) {
			p := ports[0]
			ports = ports[1:]
			return p, nil
		},
		Ready: func(context.Context, int, ForwardProc) error { return nil },
	}
	fw, err := f.Open(context.Background(), []HarnessPort{{Name: "web", HostPort: 41873}, {Name: "api", HostPort: 41874}})
	if err != nil {
		t.Fatal(err)
	}
	if len(started) != 2 {
		t.Fatalf("started %d ssh children, want one per relay", len(started))
	}
	got := fw.Forwards()
	if got[0].Name != "web" || got[0].LocalPort != 50001 || got[0].HostPort != 41873 || got[0].URL() != "http://127.0.0.1:50001" {
		t.Fatalf("forward 0 = %+v", got[0])
	}
	if got[1].LocalPort != 50002 || argValue(started[1], "-L", "")[0] != "127.0.0.1:50002:127.0.0.1:41874" {
		t.Fatalf("forward 1 = %+v, argv %v", got[1], started[1])
	}
	fw.Close()
	for i, p := range procs {
		if !p.stopped {
			t.Errorf("child %d not stopped", i)
		}
	}
}

func TestHarnessForwardRetriesFailedBindAndStopsOnError(t *testing.T) {
	var procs []*fakeForwardProc
	next := 50000
	attempts := 0
	f := HarnessForwarder{
		Target: "h",
		Start: func(context.Context, []string) (ForwardProc, error) {
			p := newFakeForwardProc()
			procs = append(procs, p)
			return p, nil
		},
		FreePort: func() (int, error) { next++; return next, nil },
		Ready: func(_ context.Context, port int, p ForwardProc) error {
			attempts++
			if port == 50001 { // lost the port race once
				p.(*fakeForwardProc).exit(errors.New("exit 255"))
				return errors.New("bind failed")
			}
			if port >= 50003 {
				return errors.New("host refused")
			}
			return nil
		},
	}
	_, err := f.Open(context.Background(), []HarnessPort{{Name: "web", HostPort: 1}, {Name: "db", HostPort: 2}})
	if err == nil {
		t.Fatal("Open succeeded although the second relay never came up")
	}
	for i, p := range procs {
		if !p.stopped {
			t.Errorf("child %d left running after a failed Open", i)
		}
	}
	if attempts < 3 {
		t.Fatalf("attempts = %d; a lost bind must be retried on a new port", attempts)
	}
}
