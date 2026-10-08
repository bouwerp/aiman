package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

var errProbe = errors.New("probe failed")

type scriptRemote struct {
	cmd      string
	cmds     []string
	out      string
	next     string
	err      error
	captured string
	calls    int
}

func (s *scriptRemote) Execute(_ context.Context, cmd string) (string, error) {
	s.calls++
	s.cmd = cmd
	s.cmds = append(s.cmds, cmd)
	if s.calls == 1 {
		if s.err != nil {
			return "", s.err
		}
		return s.out, nil
	}
	if s.next != "" {
		return s.next, nil
	}
	if s.err != nil {
		return "", s.err
	}
	return s.out, nil
}

func (s *scriptRemote) WriteFile(context.Context, string, []byte) error { return nil }

func (s *scriptRemote) CaptureTmuxPane(context.Context, string) (string, error) {
	return s.captured, nil
}

func TestTmuxPaneProbeDoesNotSendFullScrollback(t *testing.T) {
	script := tmuxPaneProbe("demo", 12, "abc")
	if strings.Contains(script, "-S - ") || strings.Contains(script, "-S - -t") {
		t.Fatalf("probe still captures the whole scrollback: %s", script)
	}
	if !strings.Contains(script, "-S -200") {
		t.Fatalf("probe should bound a first paint to 200 lines: %s", script)
	}
	if !strings.Contains(script, "UNCHANGED") || !strings.Contains(script, "sha256sum") {
		t.Fatalf("probe should short-circuit on a matching hash: %s", script)
	}
}

func TestRefreshPreviewKeepsCachedTextWhenTheProbeIsUnchanged(t *testing.T) {
	remote := &scriptRemote{out: "@@AIMAN_PANE@@ 40 deadbeef 1000 1090 24 UNCHANGED\n"}
	s := domain.Session{TmuxSession: "demo"}
	cache := PaneCache{Text: "kept", Hash: "deadbeef", HistorySize: 40}
	upd, err := RefreshPreview(context.Background(), remote, s, cache)
	if err != nil {
		t.Fatal(err)
	}
	if !upd.Unchanged || upd.Text != "kept" {
		t.Fatalf("update=%+v", upd)
	}
	if strings.Contains(remote.cmd, "-S - ") || !strings.Contains(remote.cmd, "deadbeef") {
		t.Fatalf("probe=%s", remote.cmd)
	}
}

func TestRefreshPreviewFallsBackToAFullCapture(t *testing.T) {
	remote := &scriptRemote{err: errProbe, captured: "full screen"}
	upd, err := RefreshPreview(context.Background(), remote, domain.Session{TmuxSession: "demo"}, PaneCache{})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Text != "full screen" || upd.Cache.Hash == "" {
		t.Fatalf("update=%+v", upd)
	}
}

func TestRefreshPTYSendsTheCachedHash(t *testing.T) {
	remote := &scriptRemote{out: `{"unchanged":true,"hash":"abc"}`}
	s := domain.Session{ID: "sess", Backend: domain.BackendPTY}
	upd, err := RefreshPreview(context.Background(), remote, s, PaneCache{Text: "kept", Hash: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if !upd.Unchanged || upd.Text != "kept" || !strings.Contains(remote.cmd, "--have-hash") {
		t.Fatalf("update=%+v cmd=%s", upd, remote.cmd)
	}
}

func TestRefreshPTYRequestsBoundedTail(t *testing.T) {
	remote := &scriptRemote{out: `{"unchanged":true,"hash":"abc"}`}
	s := domain.Session{ID: "sess", Backend: domain.BackendPTY}
	_, err := RefreshPreview(context.Background(), remote, s, PaneCache{Text: "kept", Hash: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(remote.cmd, "--lines 0") || !strings.Contains(remote.cmd, "--lines 200") {
		t.Fatalf("capture=%s", remote.cmd)
	}
}

func TestRefreshPTYAppliesAnAppend(t *testing.T) {
	remote := &scriptRemote{out: `{"hash":"next","append":"c"}`}
	s := domain.Session{ID: "sess", Backend: domain.BackendPTY}
	upd, err := RefreshPreview(context.Background(), remote, s, PaneCache{Text: "a\nb", Hash: "prev"})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Text != "a\nb\nc" || upd.Cache.Hash != "next" || upd.Unchanged {
		t.Fatalf("update=%+v", upd)
	}
}

func TestRefreshPTYAppliesAScroll(t *testing.T) {
	remote := &scriptRemote{out: `{"hash":"next","drop":1,"append":"d"}`}
	s := domain.Session{ID: "sess", Backend: domain.BackendPTY}
	upd, err := RefreshPreview(context.Background(), remote, s, PaneCache{Text: "a\nb\nc", Hash: "prev"})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Text != "b\nc\nd" || upd.Cache.Hash != "next" {
		t.Fatalf("update=%+v", upd)
	}
}

func TestRefreshPTYRefetchesWhenTheDropPassesTheCache(t *testing.T) {
	remote := &scriptRemote{
		out:  `{"hash":"bad","drop":9,"append":"c"}`,
		next: `{"text":"rebuilt"}`,
	}
	s := domain.Session{ID: "sess", Backend: domain.BackendPTY}
	upd, err := RefreshPreview(context.Background(), remote, s, PaneCache{Text: "a\nb", Hash: "prev"})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Text != "rebuilt" {
		t.Fatalf("update=%+v cmds=%v", upd, remote.cmds)
	}
	if len(remote.cmds) != 2 || strings.Contains(remote.cmds[1], "--lines 0") || !strings.Contains(remote.cmds[1], "--lines 200") {
		t.Fatalf("cmds=%v", remote.cmds)
	}
}

func TestParseTmuxPaneUnchanged(t *testing.T) {
	raw := "@@AIMAN_PANE@@ 40 deadbeef 1000 1090 24 UNCHANGED\n"
	upd, err := parseTmuxPane(raw, PaneCache{Text: "kept", History: "old", View: "kept", HistorySize: 40, Hash: "deadbeef"})
	if err != nil {
		t.Fatal(err)
	}
	if !upd.Unchanged || upd.Text != "kept" {
		t.Fatalf("update=%+v", upd)
	}
	if upd.SinceOutput != 90*time.Second {
		t.Fatalf("silence=%s", upd.SinceOutput)
	}
}

func TestParseTmuxPaneAppendsHistoryAndReplacesView(t *testing.T) {
	raw := strings.Join([]string{
		"@@AIMAN_PANE@@ 42 cafe 1000 1000 2 APPEND",
		"scrolled-1",
		"scrolled-2",
		"@@VIEW@@",
		"visible",
		"row",
	}, "\n")
	cache := PaneCache{History: "older", View: "old view", HistorySize: 40, Hash: "old"}
	upd, err := parseTmuxPane(raw, cache)
	if err != nil {
		t.Fatal(err)
	}
	if upd.Unchanged {
		t.Fatal("append is a change")
	}
	if upd.Cache.History != "older\nscrolled-1\nscrolled-2" || upd.Cache.View != "visible\nrow" {
		t.Fatalf("cache=%+v", upd.Cache)
	}
	if upd.Text != "older\nscrolled-1\nscrolled-2\nvisible\nrow" {
		t.Fatalf("text=%q", upd.Text)
	}
	if upd.Cache.Hash != "cafe" || upd.Cache.HistorySize != 42 {
		t.Fatalf("cursor=%+v", upd.Cache)
	}
}

func TestParseTmuxPaneViewKeepsHistory(t *testing.T) {
	raw := "@@AIMAN_PANE@@ 4 hash 10 10 1 VIEW\nnew view\n"
	cache := PaneCache{History: "kept", View: "old", HistorySize: 4}
	upd, err := parseTmuxPane(raw, cache)
	if err != nil {
		t.Fatal(err)
	}
	if upd.Cache.History != "kept" || upd.Cache.View != "new view" || upd.Text != "kept\nnew view" {
		t.Fatalf("cache=%+v text=%q", upd.Cache, upd.Text)
	}
}

func TestParseTmuxPaneReplaceSplitsViewport(t *testing.T) {
	raw := "@@AIMAN_PANE@@ 3 hash 10 10 2 REPLACE\nhist\nline\nview\nbottom\n"
	upd, err := parseTmuxPane(raw, PaneCache{})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Cache.History != "hist\nline" || upd.Cache.View != "view\nbottom" {
		t.Fatalf("cache=%+v", upd.Cache)
	}
}
