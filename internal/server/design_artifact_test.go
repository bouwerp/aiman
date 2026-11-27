package server

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/artifact"
	"github.com/bouwerp/aiman/internal/design"
)

// With no docs repository and artifacts configured, design.publish publishes
// the page as an artifact, and a second revision keeps the same artifact.
func TestDesignPublishUsesArtifactsWithoutDocsRepo(t *testing.T) {
	srv, b, wt := artifactServer(t)
	if err := os.WriteFile(filepath.Join(wt, "d.md"), []byte("# Plan\n\n```mermaid\nflowchart LR\n  A-->B\n```\n</script>\n"), 0o644); err != nil {
		t.Fatal(err)
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
	srv.SetArtifactPublisher(&artifact.Publisher{Bucket: b, Store: store, Host: "aiman.fyi", CanWrite: srv.ArtifactCanWrite})
	srv.SetDesignPublisher(&design.Publisher{Home: t.TempDir(), Run: design.ExecRun})

	var out struct {
		Type   string        `json:"type"`
		Design design.Result `json:"design"`
	}
	rpcOK(t, srv, "s1", "design.publish", map[string]any{"file": "d.md", "key": "plan"}, &out)
	if out.Design.Local || out.Design.Artifact == "" || out.Design.Revision != 1 || !strings.HasPrefix(out.Design.URL, "https://aiman.fyi/a/") {
		t.Fatalf("first publish %+v", out.Design)
	}
	id := out.Design.Artifact
	page := string(b.objs["a/"+id+"/v/1/index.html"])
	if !strings.Contains(page, `href="kit/theme.css"`) || !strings.Contains(page, "flowchart LR") {
		t.Fatalf("page does not hold the kit and the markdown:\n%s", page)
	}
	if strings.Count(page, "</script>") != 4 {
		t.Fatalf("markdown must not close a script element:\n%s", page)
	}

	rpcOK(t, srv, "s1", "design.publish", map[string]any{"file": "d.md", "key": "plan"}, &out)
	if out.Design.Artifact != id || out.Design.Revision != 2 {
		t.Fatalf("second publish should add version 2 to %s, got %+v", id, out.Design)
	}
}
