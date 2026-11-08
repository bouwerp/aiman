package ssh

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// argAfter returns the argument that follows flag in argv, or "" when flag is
// absent or last.
func argAfter(t *testing.T, argv []string, flag string) string {
	t.Helper()
	i := slices.Index(argv, flag)
	if i < 0 || i+1 >= len(argv) {
		return ""
	}
	return argv[i+1]
}

// F1: closing the attach master asks it to exit over the tty socket, never
// forwards the agent, and removes the socket file.
func TestCloseAttachMasterExitsTTYMaster(t *testing.T) {
	calls := installFakeSSH(t)
	mgr := NewManager(Config{Host: "h", User: "u"})
	tty := mgr.ttyControlPath()
	if err := os.MkdirAll(filepath.Dir(tty), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tty, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := mgr.CloseAttachMaster(context.Background()); err != nil {
		t.Fatalf("CloseAttachMaster: %v", err)
	}

	got := calls()
	if len(got) != 1 {
		t.Fatalf("want exactly one ssh call, got %d: %q", len(got), got)
	}
	argv := got[0]
	if v := argAfter(t, argv, "-O"); v != "exit" {
		t.Errorf("want -O exit, got -O %q in %q", v, argv)
	}
	if v := argAfter(t, argv, "-S"); v != tty {
		t.Errorf("want -S %q (the tty control path), got -S %q in %q", tty, v, argv)
	}
	if v := argAfter(t, argv, "-S"); v == mgr.controlPath() {
		t.Errorf("CloseAttachMaster must not touch the bulk master: %q", argv)
	}
	if !slices.Contains(argv, "u@h") {
		t.Errorf("want target u@h in %q", argv)
	}
	if slices.Contains(argv, "-A") {
		t.Errorf("closing the master must not forward the agent: %q", argv)
	}
	if _, err := os.Stat(tty); !os.IsNotExist(err) {
		t.Errorf("tty socket file should be removed, stat err = %v", err)
	}
}

// F1: a master that is already gone is not an error; any other failure is,
// and carries ssh's output.
func TestCloseAttachMasterExitStatus(t *testing.T) {
	tests := []struct {
		name    string
		exit    string
		stderr  string
		wantErr bool
	}{
		{
			name:   "control socket missing",
			exit:   "255",
			stderr: "Control socket connect(/home/u/.aiman/sockets/ssh-0123456789abcdef.sock-tty): No such file or directory",
		},
		{
			name:    "other failure",
			exit:    "255",
			stderr:  "mux_client_request_session: read from master failed: Broken pipe",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := installFakeSSH(t)
			t.Setenv("FAKE_SSH_EXIT", tc.exit)
			t.Setenv("FAKE_SSH_STDERR", tc.stderr)
			mgr := NewManager(Config{Host: "h", User: "u"})

			err := mgr.CloseAttachMaster(context.Background())
			if len(calls()) == 0 {
				t.Fatal("CloseAttachMaster made no ssh call")
			}
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("missing master should give nil, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("want an error for a non-zero exit with another message, got nil")
			}
			if !strings.Contains(err.Error(), tc.stderr) {
				t.Errorf("error should carry ssh's output %q, got %q", tc.stderr, err.Error())
			}
		})
	}
}
