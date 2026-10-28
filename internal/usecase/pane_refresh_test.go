package usecase

import (
	"context"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

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
