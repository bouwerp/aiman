package ssh

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeSSHScript records each invocation's argv, one argument per line with a
// "---" line after each call, and drains stdin so WriteFile completes. With
// FAKE_SSH_FAIL set it fails like a dropped transport, which drives Execute
// through its retries and the unmultiplexed fallback.
const fakeSSHScript = `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a" >> "$FAKE_SSH_LOG"; done
printf -- '---\n' >> "$FAKE_SSH_LOG"
cat > /dev/null
if [ -n "$FAKE_SSH_FAIL" ]; then
	echo "Connection reset by peer" >&2
	exit 255
fi
exit 0
`

// installFakeSSH puts a recording ssh first on PATH and returns a reader for
// the calls it saw.
func installFakeSSH(t *testing.T) func() [][]string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(fakeSSHScript), 0o700); err != nil { //nolint:gosec // an executable test double
		t.Fatal(err)
	}
	log := filepath.Join(dir, "calls.log")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", dir)
	t.Setenv("FAKE_SSH_LOG", log)
	return func() [][]string {
		raw, err := os.ReadFile(log) //nolint:gosec // the test's own temp file
		if err != nil {
			t.Fatalf("fake ssh was never called: %v", err)
		}
		var calls [][]string
		var cur []string
		for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
			if line == "---" {
				calls = append(calls, cur)
				cur = nil
				continue
			}
			cur = append(cur, line)
		}
		return calls
	}
}

func assertNoAgentForwarding(t *testing.T, shape string, calls [][]string) {
	t.Helper()
	if len(calls) == 0 {
		t.Fatalf("%s: no ssh calls recorded", shape)
	}
	for i, argv := range calls {
		if slices.Contains(argv, "-A") {
			t.Errorf("%s: ssh call %d forwards the agent (-A): %q", shape, i, argv)
		}
	}
}

// Bulk calls run unattended while ControlPersist keeps the master up, so they
// must never expose the laptop's ssh agent to the remote.
func TestBulkCallsDoNotForwardAgent(t *testing.T) {
	ctx := context.Background()
	newMgr := func() *Manager { return NewManager(Config{Host: "h", User: "u", Root: "/r"}) }

	t.Run("Execute", func(t *testing.T) {
		calls := installFakeSSH(t)
		if _, err := newMgr().Execute(ctx, "true"); err != nil {
			t.Fatal(err)
		}
		assertNoAgentForwarding(t, "Execute", calls())
	})
	t.Run("ExecuteWithTimeout", func(t *testing.T) {
		calls := installFakeSSH(t)
		if _, err := newMgr().ExecuteWithTimeout(ctx, "true", 0); err != nil {
			t.Fatal(err)
		}
		assertNoAgentForwarding(t, "ExecuteWithTimeout", calls())
	})
	t.Run("ExecuteRetryAndDirectFallback", func(t *testing.T) {
		calls := installFakeSSH(t)
		t.Setenv("FAKE_SSH_FAIL", "1")
		_, _ = newMgr().Execute(ctx, "true")
		got := calls()
		if !slices.ContainsFunc(got, func(a []string) bool { return slices.Contains(a, "ControlMaster=no") }) {
			t.Fatalf("the unmultiplexed fallback did not run: %q", got)
		}
		assertNoAgentForwarding(t, "Execute fallback", got)
	})
	t.Run("WriteFile", func(t *testing.T) {
		calls := installFakeSSH(t)
		if err := newMgr().WriteFile(ctx, "/tmp/x.json", []byte("{}")); err != nil {
			t.Fatal(err)
		}
		assertNoAgentForwarding(t, "WriteFile", calls())
	})
	t.Run("SessionEventsCommand", func(t *testing.T) {
		installFakeSSH(t)
		args := newMgr().SessionEventsCommand(ctx).Args
		assertNoAgentForwarding(t, "SessionEventsCommand", [][]string{args})
	})
}

// The interactive attach is the one place a human is at the keyboard and may
// need their agent on the remote (git push from the agent's shell).
func TestInteractiveAttachForwardsAgent(t *testing.T) {
	installFakeSSH(t)
	mgr := NewManager(Config{Host: "h", User: "u"})
	args := mgr.AttachPTYSession("sess").Args
	if !slices.Contains(args, "-A") {
		t.Fatalf("interactive attach should forward the agent: %q", args)
	}
}
