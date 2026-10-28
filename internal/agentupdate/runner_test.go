package agentupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type slowExec struct {
	active, max atomic.Int32
	out         string
	err         error
}

func (s *slowExec) Run(ctx context.Context, _ Command) (string, error) {
	n := s.active.Add(1)
	for {
		m := s.max.Load()
		if n <= m || s.max.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	s.active.Add(-1)
	if _, ok := ctx.Deadline(); !ok {
		return "", errors.New("no deadline")
	}
	return s.out, s.err
}

func TestRunnerSerializes(t *testing.T) {
	ex := &slowExec{out: "ok\n"}
	r := NewRunner(ex, filepath.Join(t.TempDir(), "logs", "agent-updates.log"))
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := r.Run(context.Background(), "claude", Command{Argv: []string{"claude", "update"}})
			if !res.OK {
				t.Errorf("run failed: %v", res.Err)
			}
		}()
	}
	wg.Wait()
	if ex.max.Load() != 1 {
		t.Fatalf("max concurrent %d", ex.max.Load())
	}
	b, err := os.ReadFile(r.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), "claude update") != 5 {
		t.Errorf("log:\n%s", b)
	}
}

func TestRunnerReportsNeedsRoot(t *testing.T) {
	for _, out := range []string{
		"npm error code EACCES\nnpm error syscall mkdir\n",
		"mkdir: cannot create directory '/usr/lib/node_modules/x': Permission denied\n",
	} {
		ex := &slowExec{out: out, err: errors.New("exit status 243")}
		r := NewRunner(ex, filepath.Join(t.TempDir(), "u.log"))
		res := r.Run(context.Background(), "deepcode", Command{Argv: []string{"npm", "install", "-g", "@vegamo/deepcode-cli@latest"}})
		if res.OK || !res.NeedsRoot {
			t.Fatalf("want needs root, got %+v", res)
		}
		if res.Err == nil || res.Err.Error() != "needs root: npm install -g @vegamo/deepcode-cli@latest" {
			t.Errorf("err %v", res.Err)
		}
	}
}

func TestRunnerRefusesSudo(t *testing.T) {
	ex := &slowExec{}
	r := NewRunner(ex, filepath.Join(t.TempDir(), "u.log"))
	res := r.Run(context.Background(), "claude", Command{Argv: []string{"sudo", "npm", "i", "-g", "x"}})
	if res.OK || res.Err == nil || ex.max.Load() != 0 {
		t.Fatalf("sudo ran: %+v", res)
	}
}

func TestRunnerPlainFailure(t *testing.T) {
	ex := &slowExec{out: "network down\n", err: errors.New("exit status 1")}
	r := NewRunner(ex, filepath.Join(t.TempDir(), "u.log"))
	res := r.Run(context.Background(), "grok", Command{Argv: []string{"grok", "update"}})
	if res.OK || res.NeedsRoot || res.Err == nil || !strings.Contains(res.Output, "network down") {
		t.Fatalf("%+v", res)
	}
}
