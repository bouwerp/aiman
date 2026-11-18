package edge

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// rawUpstream accepts one connection, reads a request's headers, answers
// its headers at once and then calls rest with the connection and the
// request's Content-Length.
func rawUpstream(t *testing.T, rest func(c net.Conn, br *bufio.Reader, length int)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		br := bufio.NewReader(c)
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		if _, err := io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: application/x-amz-json-1.0\r\nContent-Length: 5\r\n\r\n"); err != nil {
			return
		}
		rest(c, br, int(req.ContentLength))
	}()
	return ln.Addr().String()
}

// echoAfterBody reads the whole request body, reports it on got and answers
// "hello".
func echoAfterBody(got chan<- string) func(c net.Conn, br *bufio.Reader, length int) {
	return func(c net.Conn, br *bufio.Reader, length int) {
		body := make([]byte, length)
		if _, err := io.ReadFull(br, body); err != nil {
			got <- "read: " + err.Error()
			return
		}
		got <- string(body)
		_, _ = io.WriteString(c, "hello")
	}
}

// rawPost sends a SendMessage POST with a 10-byte body to addr: the headers
// and the first half at once, the second half after a pause, so the
// upstream's answer starts before the forwarder has read the whole body. It
// returns the answer's status and body and the error reading it.
func rawPost(t *testing.T, addr, target string) (int, string, error) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	head := "POST " + target + " HTTP/1.1\r\nHost: aws:4566\r\nX-Amz-Target: AmazonSQS.SendMessage\r\n" +
		"Content-Type: application/x-amz-json-1.0\r\nContent-Length: 10\r\n\r\n"
	if _, err := io.WriteString(c, head+`{"a":`); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(c, `"b"}`+" ")
	}()
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), err
}

// TestAWSTapAnswerBeforeBodyEnds keeps the request body with the transport
// when the emulator answers before the tap has read all of it: the server
// must not drain and close the body when the answer's headers go out, which
// made the transport close the emulator connection under the answer and the
// caller read an unexpected EOF. Which goroutine reads the rest of the body
// first varies, so it runs a few rounds.
func TestAWSTapAnswerBeforeBodyEnds(t *testing.T) {
	for round := range 8 {
		got := make(chan string, 1)
		tap, u := startTap(t, rawUpstream(t, echoAfterBody(got)), WithAWSTapResolver(noLookup))
		status, body, err := rawPost(t, strings.TrimPrefix(u, "http://"), "/")
		if err != nil || status != 200 || body != "hello" {
			t.Fatalf("round %d: answer %d %q %v", round, status, body, err)
		}
		if b := <-got; b != `{"a":"b"} ` {
			t.Fatalf("round %d: the emulator got %q", round, b)
		}
		calls := waitCalls(t, tap, 1)
		if c := calls[0]; c.Status != 200 || c.Error != "" || c.RequestBytes != 10 || c.ResponseBytes != 5 || c.Operation != "SendMessage" {
			t.Fatalf("round %d: call %+v", round, c)
		}
	}
}

// TestProxyAnswerBeforeBodyEnds is the same for the online capture proxy,
// which forwards the same way.
func TestProxyAnswerBeforeBodyEnds(t *testing.T) {
	for round := range 8 {
		got := make(chan string, 1)
		_, pu := testProxy(t, ProxyConfig{Online: true, DialUpstream: dialTo(rawUpstream(t, echoAfterBody(got)))})
		status, body, err := rawPost(t, pu.Host, "http://upstream.test/x")
		if err != nil || status != 200 || body != "hello" {
			t.Fatalf("round %d: answer %d %q %v", round, status, body, err)
		}
		if b := <-got; b != `{"a":"b"} ` {
			t.Fatalf("round %d: upstream got %q", round, b)
		}
	}
}

// TestAWSTapLogsCutOffAnswer ends a call whose answer the emulator cut off
// in the log, with the error, instead of leaving it pending.
func TestAWSTapLogsCutOffAnswer(t *testing.T) {
	up := rawUpstream(t, func(c net.Conn, br *bufio.Reader, length int) {
		_, _ = io.ReadFull(br, make([]byte, length))
		_, _ = io.WriteString(c, "he")
	})
	tap, u := startTap(t, up, WithAWSTapResolver(noLookup))
	if _, body, err := rawPost(t, strings.TrimPrefix(u, "http://"), "/"); err == nil {
		t.Fatalf("a cut off answer read whole: %q", body)
	}
	calls := waitCalls(t, tap, 1)
	if c := calls[0]; c.Pending || c.Status != 200 || !strings.HasPrefix(c.Error, "answer cut off: ") || c.ResponseBytes != 2 {
		t.Fatalf("call %+v", c)
	}
}

func noLookup(context.Context, string) ([]string, error) { return nil, nil }
