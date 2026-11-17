package images_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness/images"
)

func TestProxyAllowsOnlyProvider(t *testing.T) {
	var mu sync.Mutex
	var dialed []string
	p := &images.Proxy{
		Allow: []string{"api.anthropic.com:443"},
		Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, addr)
			mu.Unlock()
			c, s := net.Pipe()
			go func() { // an upstream that echoes
				_, _ = io.Copy(s, s)
				_ = s.Close()
			}()
			return c, nil
		},
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Serve(ctx, l) }()

	ask := func(req string) (int, net.Conn, *bufio.Reader) {
		t.Helper()
		c, err := net.DialTimeout("tcp", l.Addr().String(), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(c, req); err != nil {
			t.Fatal(err)
		}
		br := bufio.NewReader(c)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("%q: %v", req, err)
		}
		return resp.StatusCode, c, br
	}

	code, c, br := ask("CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n")
	if code != http.StatusOK {
		t.Fatalf("allowed host: %d", code)
	}
	if _, err := io.WriteString(c, "ping\n"); err != nil {
		t.Fatal(err)
	}
	if line, err := br.ReadString('\n'); err != nil || line != "ping\n" {
		t.Fatalf("tunnel echo = %q, %v", line, err)
	}
	_ = c.Close()

	for _, req := range []string{
		"CONNECT evil.example:443 HTTP/1.1\r\nHost: evil.example:443\r\n\r\n",
		"CONNECT api.anthropic.com:80 HTTP/1.1\r\nHost: api.anthropic.com:80\r\n\r\n",
		"CONNECT API.ANTHROPIC.COM.evil.example:443 HTTP/1.1\r\nHost: x\r\n\r\n",
		"CONNECT 169.254.169.254:443 HTTP/1.1\r\nHost: x\r\n\r\n",
		"GET http://api.anthropic.com/ HTTP/1.1\r\nHost: api.anthropic.com\r\n\r\n",
	} {
		code, c, _ := ask(req)
		_ = c.Close()
		if code != http.StatusForbidden {
			t.Errorf("%q: status %d, want 403", strings.SplitN(req, "\r\n", 2)[0], code)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(dialed) != "[api.anthropic.com:443]" {
		t.Fatalf("dialed %v, want only the provider", dialed)
	}
}
