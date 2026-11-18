package harness

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness/awsiac"
	"github.com/bouwerp/aiman/internal/harness/edge"
)

// TestAWSDockerWrite brings a seeded run up for real, writes an object, an
// item and a message as the operator through the applier and the tap,
// reads the object and item back through the read relay, then resets: the
// written data is gone and the seeded data is back.
func TestAWSDockerWrite(t *testing.T) {
	needDocker(t)
	useDeclared(t)
	ensureImage(t, dockerTestImage)
	ensureImage(t, EdgeImage)
	ensureImage(t, MiniStackImage)
	repo := awsRepo(t, awsPlanFile, nil)
	var mu sync.Mutex
	var rows []map[string]any
	m, err := NewManager(Options{
		Dir:      filepath.Join(t.TempDir(), "harness"),
		AimanBin: edgeBinary(t),
		UID:      os.Getuid(), GID: os.Getgid(),
		AllowedImages: []string{"busybox:*", MiniStackImage},
		Docker:        ExecDocker{},
		AppImage:      func(string, string) (string, error) { return dockerTestImage, nil },
		Record: func(_ string, _ domain.EventActor, kind domain.EventKind, payload map[string]any) {
			if kind == domain.EventHarnessAWSWrite {
				mu.Lock()
				rows = append(rows, payload)
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	ctx := context.Background()
	run, err := m.Up(ctx, UpRequest{Session: "s1", Repo: repo, Scenario: "seeded", Actor: domain.EventActorUser})
	if err != nil {
		t.Fatal(err)
	}
	project := "harness-" + run.ID
	t.Cleanup(func() {
		_ = exec.Command("docker", "compose", "-p", project, "--profile", "*", "down", "-v", "--remove-orphans", "-t", "1").Run()
	})
	run = m.Wait(ctx, run.ID, 4*time.Minute)
	if run.State != StateReady {
		t.Fatalf("run %s: %s: %s", run.State, run.FailedStep, run.Error)
	}
	physical := map[string]string{}
	for _, r := range run.AWS.Resources {
		physical[r.LogicalID] = r.Physical
	}
	bucket, table, queue := physical["UploadBucket"], physical["OrdersTable"], physical["Events"]
	write := func(req AWSWriteRequest) AWSWriteResult {
		t.Helper()
		req.Run, req.Operator, req.Actor = run.ID, true, domain.EventActorUser
		res, err := m.AWSWrite(ctx, req)
		if err != nil {
			t.Fatalf("%s: %v", req.Op, err)
		}
		return res
	}
	read := func(req AWSReadRequest) (AWSResult, error) {
		req.Run = run.ID
		return m.AWS(ctx, req)
	}
	const secret = "operator-written-value"

	put := write(AWSWriteRequest{Op: awsiac.WriteS3Put, Bucket: bucket, Key: "op/new.txt", ContentType: "text/plain", Data: []byte(secret)})
	if put.ETag == "" || put.VersionID == "" {
		t.Fatalf("put %+v", put)
	}
	obj, err := read(AWSReadRequest{Op: AWSOpS3Object, Bucket: bucket, Key: "op/new.txt"})
	if err != nil || obj.Object == nil || obj.Object.Data != secret {
		t.Fatalf("read back %+v %v", obj.Object, err)
	}
	write(AWSWriteRequest{Op: awsiac.WriteDDBPut, Table: table, Item: map[string]any{"pk": "op1", "status": "new", "note": secret}})
	item, err := read(AWSReadRequest{Op: AWSOpDDBGet, Table: table, ItemKey: map[string]any{"pk": "op1"}})
	if err != nil || item.Found == nil || !*item.Found || item.Item["note"] != secret {
		t.Fatalf("item %+v %v", item, err)
	}
	if msg := write(AWSWriteRequest{Op: awsiac.WriteSQSSend, Queue: queue, Body: secret}); msg.MessageID == "" {
		t.Fatalf("send %+v", msg)
	}
	del := write(AWSWriteRequest{Op: awsiac.WriteDDBDelete, Table: table, ItemKey: map[string]any{"pk": "o2"}})
	if del.Found == nil || !*del.Found {
		t.Fatalf("delete %+v", del)
	}

	// The tap logged the writes as the operator's.
	calls, _, err := m.AWSCalls(ctx, run.ID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	ops := map[string]bool{}
	for _, c := range calls {
		if c.Client == edge.AWSClientOperator {
			ops[c.Operation] = true
		}
	}
	for _, op := range []string{"PutObject", "PutItem", "GetQueueUrl", "SendMessage", "DeleteItem"} {
		if !ops[op] {
			t.Errorf("no %s from %s in the call log: %v", op, edge.AWSClientOperator, ops)
		}
	}

	reset := write(AWSWriteRequest{Op: awsiac.WriteReset})
	if reset.Resources != 6 || reset.Calls == 0 || reset.FailedStep != "" {
		t.Fatalf("reset %+v", reset)
	}
	if _, err := read(AWSReadRequest{Op: AWSOpS3Object, Bucket: bucket, Key: "op/new.txt"}); !errors.Is(err, ErrParams) {
		t.Fatalf("the written object survived the reset: %v", err)
	}
	if obj, err = read(AWSReadRequest{Op: AWSOpS3Object, Bucket: bucket, Key: "seed/a.txt"}); err != nil || obj.Object.Data != "hello" {
		t.Fatalf("seeded object after reset %+v %v", obj.Object, err)
	}
	if item, err = read(AWSReadRequest{Op: AWSOpDDBGet, Table: table, ItemKey: map[string]any{"pk": "op1"}}); err != nil || *item.Found {
		t.Fatalf("written item after reset %+v %v", item, err)
	}
	if item, err = read(AWSReadRequest{Op: AWSOpDDBGet, Table: table, ItemKey: map[string]any{"pk": "o2"}}); err != nil || !*item.Found {
		t.Fatalf("seeded item after reset %+v %v", item, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(rows) != 5 {
		t.Fatalf("ledger rows %d: %v", len(rows), rows)
	}
	for _, r := range rows {
		for k, v := range r {
			if s, ok := v.(string); ok && (strings.Contains(s, secret) || strings.Contains(s, "op1") || strings.Contains(s, "op/new")) {
				t.Errorf("ledger %s holds a value: %v", k, r)
			}
		}
	}
	if err := m.Down(ctx, run.ID, StopDown); err != nil {
		t.Fatal(err)
	}
}
