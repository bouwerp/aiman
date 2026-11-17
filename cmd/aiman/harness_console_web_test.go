package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/console"
	"github.com/bouwerp/aiman/internal/usecase"
)

type fakeWebRPC struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeWebRPC) Call(_ context.Context, method string, _ map[string]any) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, method)
	f.mu.Unlock()
	if method == "harness.status" {
		return json.RawMessage(strings.Replace(consoleRun, "%s", "ready", 1)), nil
	}
	return json.RawMessage(`{}`), nil
}

func TestHarnessConsoleWeb(t *testing.T) {
	var procs []*consoleProc
	var mu sync.Mutex
	next := 53010
	fwd := usecase.HarnessForwarder{
		Target: "code@regent0",
		Start: func(_ context.Context, _ []string) (usecase.ForwardProc, error) {
			mu.Lock()
			defer mu.Unlock()
			p := &consoleProc{done: make(chan struct{})}
			procs = append(procs, p)
			return p, nil
		},
		FreePort: func() (int, error) { mu.Lock(); defer mu.Unlock(); next++; return next, nil },
		Ready:    func(context.Context, int, usecase.ForwardProc) error { return nil },
	}
	opened := make(chan string, 1)
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- harnessWebConsole(ctx, harnessArgs{session: "s1"}, webConsoleDeps{
			rpc:       &fakeWebRPC{},
			forwarder: console.SSHForwarder{F: fwd},
			listen:    func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") },
			open:      func(u string) error { opened <- u; return nil },
			out:       &out,
			log:       io.Discard,
		})
	}()
	var page string
	select {
	case page = <-opened:
	case err := <-done:
		t.Fatalf("console ended early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("browser never opened")
	}
	u, err := url.Parse(page)
	if err != nil || u.Hostname() != "127.0.0.1" || !strings.HasPrefix(u.Fragment, "token=") {
		t.Fatalf("console URL %q", page)
	}
	token := strings.TrimPrefix(u.Fragment, "token=")

	get := func(tok string) (*http.Response, []byte) {
		req, _ := http.NewRequest("GET", "http://"+u.Host+"/api/state", nil)
		if tok != "" {
			req.Header.Set(console.TokenHeader, tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}
	if resp, _ := get(""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, body := get(token)
		if strings.Contains(string(body), `"url":"http://127.0.0.1:53011"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("forwards never showed: %s", body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(out.String(), page) {
		t.Errorf("URL not printed:\n%s", out.String())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("console: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("console did not stop on cancel")
	}
	mu.Lock()
	defer mu.Unlock()
	for i, p := range procs {
		select {
		case <-p.done:
		default:
			t.Errorf("forward %d left running", i)
		}
	}
	if _, err := http.Get("http://" + u.Host + "/"); err == nil {
		t.Error("console still listening after it stopped")
	}
}

func TestHarnessConsoleModes(t *testing.T) {
	a, err := parseHarnessArgs([]string{"--session", "s1", "--text"})
	if err != nil || !a.text {
		t.Fatalf("--text: %v %+v", err, a)
	}
	a, err = parseHarnessArgs([]string{"--session", "s1", "--no-browser"})
	if err != nil || !a.noBrowser {
		t.Fatalf("--no-browser: %v %+v", err, a)
	}
}

func TestHarnessConsoleOpener(t *testing.T) {
	for goos, want := range map[string]string{"darwin": "open", "linux": "xdg-open", "freebsd": "xdg-open", "windows": ""} {
		if got := browserOpener(goos); got != want {
			t.Errorf("%s: %q, want %q", goos, got, want)
		}
	}
}
