package ssh

import (
	"strings"
	"testing"
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
