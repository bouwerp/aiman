package server

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/artifact"
)

// Acceptance tests for docs/specs/artifacts.md piece C (share RPCs). Do not edit.

func TestArtifactShareRPCs(t *testing.T) {
	srv, b, _ := artifactServer(t)
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
	var pub struct {
		Artifact artifact.Published `json:"artifact"`
	}
	rpcOK(t, srv, "s1", "artifact.publish", map[string]any{"page": "report/index.html"}, &pub)
	id := pub.Artifact.ID

	var share struct {
		Type  string             `json:"type"`
		Share artifact.ShareList `json:"share"`
	}
	rpcOK(t, srv, "", "artifact.share", map[string]any{"id": id, "emails": []string{"Lead@Partner.com"}, "domains": []string{"example.com"}}, &share)
	if share.Type != "artifact_share" || len(share.Share.Emails) != 1 || share.Share.Emails[0] != "lead@partner.com" {
		t.Fatalf("share %+v", share)
	}
	var meta artifact.Meta
	if err := json.Unmarshal(b.objs["a/"+id+"/meta.json"], &meta); err != nil || len(meta.Share.Domains) != 1 {
		t.Fatalf("meta %+v %v", meta, err)
	}
	rpcOK(t, srv, "", "artifact.unshare", map[string]any{"id": id, "all": true}, &share)
	if len(share.Share.Emails)+len(share.Share.Domains) != 0 {
		t.Fatalf("unshare %+v", share)
	}
	for _, m := range []string{"artifact.unshare", "artifact.delete"} {
		if resp := rpc(t, srv, "s1", m, map[string]any{"id": id}); resp.Error == nil || resp.Error.Code != CodeNeedsOperator {
			t.Fatalf("session %s: %+v", m, resp.Error)
		}
	}
	var del struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	rpcOK(t, srv, "", "artifact.delete", map[string]any{"id": id}, &del)
	if del.Type != "artifact_deleted" || del.ID != id {
		t.Fatalf("delete %+v", del)
	}
	if resp := rpc(t, srv, "", "artifact.share", map[string]any{"id": "zzzzzzzzzzzzzzzzzzzz", "emails": []string{"a@b.c"}}); resp.Error == nil || resp.Error.Code != CodeNotFound {
		t.Fatalf("unknown id: %+v", resp.Error)
	}
}
