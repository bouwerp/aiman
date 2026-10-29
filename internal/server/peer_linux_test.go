//go:build linux

package server

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPeerPIDOfUnixConn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := net.Dial("unix", path)
		if err == nil {
			defer c.Close()
			<-make(chan struct{})
		}
	}()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if pid, ok := peerPID(conn); !ok || pid != os.Getpid() {
		t.Fatalf("peerPID = %d, %v; want %d", pid, ok, os.Getpid())
	}
}
