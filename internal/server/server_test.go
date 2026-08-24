package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPing(t *testing.T) {
	dir := t.TempDir()
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := New(ln, nil, nil, nil, nil, nil, "test")
	go func() { _ = srv.Serve(ctx) }()

	conn, err := net.Dial("unix", SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Write([]byte(`{"id":"1","method":"ping","params":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("decode %s: %v", line, err)
	}
	if resp.Error != nil {
		t.Fatalf("error: %+v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var pong map[string]any
	if err := json.Unmarshal(raw, &pong); err != nil {
		t.Fatal(err)
	}
	if pong["type"] != "pong" {
		t.Fatalf("result = %s", raw)
	}
	if int(pong["protocol"].(float64)) != ProtocolVersion {
		t.Fatalf("protocol = %v", pong["protocol"])
	}
}

func TestListenSocketMode(t *testing.T) {
	dir := t.TempDir()
	l, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	st, err := os.Stat(SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %o, want 0600", st.Mode().Perm())
	}
	if filepath.Base(SocketPath(dir)) != "aiman.sock" {
		t.Fatal(SocketPath(dir))
	}
}
