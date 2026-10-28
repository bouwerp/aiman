package agentupdate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// prefixHost fakes a host where deepcode sits in the root-owned system npm
// prefix until the user prefix is configured and the package reinstalled.
type prefixHost struct {
	mu     sync.Mutex
	prefix string
	user   bool
	ran    []string
}

func (h *prefixHost) Run(_ context.Context, c Command) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	j := strings.Join(c.Argv, " ")
	switch {
	case strings.Contains(j, "command -v deepcode"):
		if h.user {
			return "/home/u/.npm-global/lib/node_modules/@vegamo/deepcode-cli/dist/cli.js\n", nil
		}
		return "/usr/lib/node_modules/@vegamo/deepcode-cli/dist/cli.js\n", nil
	case j == "deepcode --version":
		if h.user {
			return "0.6.2\n", nil
		}
		return "0.6.1\n", nil
	case strings.HasPrefix(j, "npm view"):
		return "0.6.2\n", nil
	case strings.HasPrefix(j, "npm config set prefix "):
		h.ran = append(h.ran, j)
		h.prefix = c.Argv[4]
		return "", nil
	case strings.HasPrefix(j, "npm install -g "):
		h.ran = append(h.ran, j)
		if h.prefix == "" {
			return "npm ERR! code EACCES\n", errors.New("exit status 243")
		}
		h.user = true
		return "added 1 package\n", nil
	}
	return "", errors.New("exit status 127")
}

func prefixManager(t *testing.T, h *prefixHost) *Manager {
	t.Helper()
	pol, err := NewPolicy("notify", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := Lookup("deepcode")
	c, _ := Lookup("claude")
	dir := t.TempDir()
	return &Manager{
		Prober:    &Prober{Exec: h, HTTP: &fakeHTTP{}},
		Runner:    NewRunner(h, filepath.Join(dir, "log")),
		Entries:   []Entry{d, c},
		StatePath: filepath.Join(dir, "state.json"),
		Policy:    pol,
		Home:      "/home/u",
		RootOwned: func(p string) bool { return strings.HasPrefix(p, "/usr/") },
		Logf:      t.Logf,
	}
}

func TestMovePrefixReinstallsUnderUserPrefix(t *testing.T) {
	h := &prefixHost{}
	m := prefixManager(t, h)
	ctx := context.Background()
	list, _ := m.Status(ctx, true)
	before := statusOf(t, list, "deepcode")
	if !before.RootOwned || !CanMovePrefix(before) {
		t.Fatalf("before %+v", before)
	}
	out, err := m.MovePrefix(ctx, "deepcode")
	if err != nil || !out.Accepted {
		t.Fatalf("move %+v %v", out, err)
	}
	m.Wait()
	want := []string{"npm config set prefix /home/u/.npm-global", "npm install -g @vegamo/deepcode-cli@latest"}
	if strings.Join(h.ran, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ran %q, want %q", h.ran, want)
	}
	list, _ = m.Status(ctx, false)
	after := statusOf(t, list, "deepcode")
	if after.RootOwned || after.Status != StatusCurrent || !strings.HasPrefix(after.Path, "/home/u/.npm-global/") || CanMovePrefix(after) {
		t.Fatalf("after %+v", after)
	}
}

func TestMovePrefixRefusals(t *testing.T) {
	h := &prefixHost{}
	m := prefixManager(t, h)
	ctx := context.Background()
	if _, err := m.MovePrefix(ctx, "nosuch"); err == nil {
		t.Fatal("unknown agent accepted")
	}
	m.Entries = append(m.Entries, Entry{Agent: "agy"})
	if _, err := m.MovePrefix(ctx, "agy"); err == nil {
		t.Fatal("agent without a package accepted")
	}
	m.Sessions = func(context.Context) ([]LiveSession, error) {
		return []LiveSession{{Agent: "deepcode", HookState: "idle"}}, nil
	}
	out, err := m.MovePrefix(ctx, "deepcode")
	if err != nil || out.Accepted || !strings.HasPrefix(out.Reason, "deferred: ") {
		t.Fatalf("busy move %+v %v", out, err)
	}
	m.Home = ""
	if _, err := m.MovePrefix(ctx, "deepcode"); err == nil {
		t.Fatal("move without a home accepted")
	}
	if len(h.ran) != 0 {
		t.Fatalf("ran %q", h.ran)
	}
}

func TestMovePrefixCommandsForDisplay(t *testing.T) {
	d, _ := Lookup("deepcode")
	var got []string
	for _, c := range MovePrefixCommands("~", d) {
		got = append(got, c.String())
	}
	want := "npm config set prefix ~/.npm-global\nnpm install -g @vegamo/deepcode-cli@latest"
	if strings.Join(got, "\n") != want {
		t.Fatalf("commands %q", got)
	}
	for _, c := range MovePrefixCommands("/home/u", d) {
		if privileged(c.Argv[0]) || strings.Contains(c.String(), "uninstall") {
			t.Fatalf("command %q", c.String())
		}
	}
}
