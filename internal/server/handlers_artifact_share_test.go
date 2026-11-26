package server

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/artifact"
	"github.com/bouwerp/aiman/internal/domain"
)

func TestArtifactShareLedgerHoldsNoAddresses(t *testing.T) {
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
	if resp := rpc(t, srv, "s1", "artifact.share", map[string]any{"id": id, "emails": []string{"x@y.z"}}); resp.Error == nil || resp.Error.Code != CodeNeedsOperator {
		t.Fatalf("session share: %+v", resp.Error)
	}
	rpcOK(t, srv, "", "artifact.share", map[string]any{"id": id, "emails": []string{"lead@partner.com", " "}, "domains": []string{"partner.com"}}, nil)
	rpcOK(t, srv, "", "artifact.unshare", map[string]any{"id": id, "emails": []string{"lead@partner.com"}}, nil)
	rpcOK(t, srv, "", "artifact.delete", map[string]any{"id": id}, nil)

	lister, ok := srv.repo.(sessionEventLister)
	if !ok {
		t.Fatal("repository has no ledger")
	}
	rows, err := lister.ListSessionEvents(context.Background(), domain.ArtifactsLedger, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, r := range rows {
		kinds = append(kinds, string(r.Kind))
		if strings.Contains(string(r.Payload), "partner") {
			t.Fatalf("ledger row carries an address: %s", r.Payload)
		}
		if !strings.Contains(string(r.Payload), id) {
			t.Fatalf("ledger row without the id: %s", r.Payload)
		}
	}
	if strings.Join(kinds, ",") != "artifact.shared,artifact.shared,artifact.deleted" {
		t.Fatalf("kinds %v", kinds)
	}
	if !strings.Contains(string(rows[0].Payload), `"emails":1`) || !strings.Contains(string(rows[1].Payload), `"emails":0`) {
		t.Fatalf("counts: %s / %s", rows[0].Payload, rows[1].Payload)
	}
}
