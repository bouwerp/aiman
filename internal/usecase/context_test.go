package usecase

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

type fakeContextRemote struct {
	out   string
	err   error
	cmds  []string
	wrote map[string][]byte
}

func (f *fakeContextRemote) Execute(_ context.Context, cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	if strings.Contains(cmd, `printf %s "$HOME"`) {
		return "/home/dev", f.err
	}
	return f.out, f.err
}

func (f *fakeContextRemote) WriteFile(_ context.Context, path string, content []byte) error {
	if f.wrote == nil {
		f.wrote = map[string][]byte{}
	}
	f.wrote[path] = content
	return nil
}

func TestParseContextPackOutput(t *testing.T) {
	got := parseContextPackOutput("{\n  \"type\": \"context_pack\",\n  \"text\": \"# Shared context\\n\\n- **n** (`id`): a\\n\"\n}")
	if !strings.Contains(got, "Shared context") {
		t.Fatalf("%q", got)
	}
	if parseContextPackOutput("") != "" {
		t.Fatal("empty")
	}
}

func TestInjectSharedContext(t *testing.T) {
	r := &fakeContextRemote{out: `{"type":"context_pack","text":"# Shared context\n\n- note\n"}`}
	got := InjectSharedContext(context.Background(), r, "/wt", "G1", "org/repo", "Read task")
	if !strings.Contains(got, domain.AimanContextFileName) {
		t.Fatalf("prompt %q", got)
	}
	body := string(r.wrote["/wt/"+domain.AimanContextFileName])
	if !strings.Contains(body, "Shared context") {
		t.Fatalf("file %s", body)
	}
}

func TestPutSnapshotContext(t *testing.T) {
	r := &fakeContextRemote{}
	err := PutSnapshotContext(context.Background(), r, &domain.SessionSnapshot{
		SessionID: "sid",
		Summary:   "Fixed auth",
		IssueKey:  "WTB-9",
		Overview:  []string{"did x"},
	}, "WTB-9")
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := filepath.Join("/home/dev", config.DirName, "context", "groups", "WTB-9")
	found := false
	for p, body := range r.wrote {
		if strings.HasPrefix(p, wantPrefix) && strings.Contains(string(body), "Fixed auth") {
			found = true
		}
	}
	if !found {
		t.Fatalf("writes %+v", r.wrote)
	}
}
