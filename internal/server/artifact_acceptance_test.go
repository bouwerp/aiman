package server

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/artifact"
)

// Acceptance tests for docs/specs/artifacts.md piece A (RPC). Do not edit.

type memBucket struct {
	mu   sync.Mutex
	objs map[string][]byte
	n    int
}

func (b *memBucket) Get(_ context.Context, key string) ([]byte, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, ok := b.objs[key]
	if !ok {
		return nil, "", artifact.ErrNotFound
	}
	return d, `"` + key + `"`, nil
}

func (b *memBucket) Put(_ context.Context, key string, data []byte, _ string, _ artifact.Cond) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objs[key] = append([]byte(nil), data...)
	return `"` + key + `"`, nil
}

func (b *memBucket) Delete(_ context.Context, keys []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, k := range keys {
		delete(b.objs, k)
	}
	return nil
}

func (b *memBucket) List(_ context.Context, prefix string) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for k := range b.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

func artifactServer(t *testing.T) (*Server, *memBucket, string) {
	t.Helper()
	root := t.TempDir()
	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(filepath.Join(wt, "report"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "report", "index.html"), []byte("<p>hi</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "report", "chart.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.yaml"), []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "secret.yaml"), filepath.Join(wt, "report", "leak.txt")); err != nil {
		t.Fatal(err)
	}
	sess := todoSession("s1")
	sess.WorkingDirectory = wt
	srv, _, _ := todoServer(t, sess)
	b := &memBucket{objs: map[string][]byte{}}
	return srv, b, wt
}

func TestArtifactRPC(t *testing.T) {
	srv, b, _ := artifactServer(t)
	if resp := rpc(t, srv, "s1", "artifact.publish", map[string]any{"page": "report/index.html"}); resp.Error == nil || resp.Error.Code != CodeUnavailable {
		t.Fatalf("no publisher: %+v", resp.Error)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := artifact.OpenSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetArtifactPublisher(&artifact.Publisher{Bucket: b, Store: store, Host: "aiman.fyi"})

	var out struct {
		Type     string             `json:"type"`
		Artifact artifact.Published `json:"artifact"`
	}
	rpcOK(t, srv, "s1", "artifact.publish", map[string]any{
		"page": "report/index.html", "title": "Load test",
		"files": []map[string]string{{"from": "report/chart.svg"}},
	}, &out)
	if out.Type != "artifact" || out.Artifact.Version != 1 || !strings.HasPrefix(out.Artifact.URL, "https://aiman.fyi/a/") {
		t.Fatalf("publish %+v", out)
	}
	id := out.Artifact.ID
	if string(b.objs["a/"+id+"/v/1/index.html"]) != "<p>hi</p>" || string(b.objs["a/"+id+"/v/1/chart.svg"]) != "<svg/>" {
		t.Fatalf("objects %v", keys(b.objs))
	}

	// A file that resolves outside the worktree is refused.
	resp := rpc(t, srv, "s1", "artifact.publish", map[string]any{"page": "report/index.html", "files": []map[string]string{{"from": "report/leak.txt"}}})
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("symlink out: %+v", resp.Error)
	}

	var list struct {
		Type      string           `json:"type"`
		Artifacts []map[string]any `json:"artifacts"`
	}
	rpcOK(t, srv, "s1", "artifact.list", map[string]any{}, &list)
	if list.Type != "artifacts" || len(list.Artifacts) != 1 || list.Artifacts[0]["id"] != id {
		t.Fatalf("list %+v", list)
	}

	// Share is the operator's.
	resp = rpc(t, srv, "s1", "artifact.share", map[string]any{"id": id, "emails": []string{"a@b.com"}})
	if resp.Error == nil || resp.Error.Code != CodeNeedsOperator {
		t.Fatalf("session share: %+v", resp.Error)
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
