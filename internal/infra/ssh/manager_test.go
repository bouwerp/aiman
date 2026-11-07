package ssh

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBulkSSHArgsCompressesWithoutX11(t *testing.T) {
	args := strings.Join(bulkSSHArgs("/tmp/sock", true), " ")
	if !strings.Contains(args, "Compression=yes") || !strings.Contains(args, "ServerAliveInterval=30") {
		t.Fatalf("bulk ssh should compress and keep a 30s alive interval: %s", args)
	}
	if strings.Contains(args, "-X") {
		t.Fatalf("bulk ssh must not forward X11: %s", args)
	}
}

func TestAttachPTYSessionSkipsX11(t *testing.T) {
	mgr := NewManager(Config{Host: "example.com", User: "code"})
	args := strings.Join(mgr.AttachPTYSession("sess").Args, " ")
	if strings.Contains(args, " -X ") || strings.Contains(args, "Compression=yes") {
		t.Fatalf("pty attach should stay uncompressed and without X11: %s", args)
	}
	if !strings.Contains(args, "-tty") {
		t.Fatalf("pty attach should use its own tty master: %s", args)
	}
}

// A session on a connection that forwards the agent can reach the agent even
// when its own client did not ask for it, so bulk calls must never share the
// attach master, and must override a ForwardAgent line in the user's ssh config.
func TestBulkConnectionsStayApartFromAgentForwarding(t *testing.T) {
	mgr := NewManager(Config{Host: "example.com", User: "code"})

	attach := mgr.AttachPTYSession("sess").Args
	bulk := mgr.controlPath()
	if slices.Contains(attach, bulk) {
		t.Fatalf("attach must not use the bulk master %s: %q", bulk, attach)
	}

	shapes := map[string][]string{
		"bulk multiplexed": bulkSSHArgs(bulk, true),
		"bulk direct":      bulkSSHArgs(bulk, false),
		"events stream":    mgr.SessionEventsCommand(context.Background()).Args,
	}
	for name, args := range shapes {
		if !slices.Contains(args, noAgentForwarding) {
			t.Errorf("%s must set %s: %q", name, noAgentForwarding, args)
		}
	}
}

// PTY attach is a raw byte relay over ssh: a terminal emulator in between
// would reinterpret the stream the agent is drawing.
func TestAttachPTYSessionRunsOverSSH(t *testing.T) {
	mgr := NewManager(Config{Host: "example.com", User: "code"})
	cmd := mgr.AttachPTYSession("sess")
	args := strings.Join(cmd.Args, " ")
	if cmd.Args[0] != "ssh" || !strings.Contains(args, "pty attach") {
		t.Fatalf("pty attach must run aiman pty attach over ssh: %s", args)
	}
}

// A multiplexed ssh client hands its output pipe to the control master, which
// holds it until the remote command ends. The fake client leaves the pipe with
// a background process the same way.
func TestExecuteTimeoutReturnsWhileSomethingHoldsTheOutputPipe(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "ssh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 30 &\nwait\n"), 0o700); err != nil { //nolint:gosec // an executable test double
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", dir)
	m := NewManager(Config{Host: "h", User: "u", Root: "/r"})
	start := time.Now()
	_, err := m.ExecuteWithTimeout(context.Background(), "slow", 200*time.Millisecond)
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Fatalf("returned after %s: the timeout did not hold", took)
	}
}
