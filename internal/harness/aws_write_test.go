package harness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness/awsiac"
)

// answerWrites makes the fake docker answer the applier's --write runs with
// reply, recording each write it was handed.
func answerWrites(tm *testManager, reply func(w awsiac.Write) awsApplyReply) *[]awsiac.Write {
	var got []awsiac.Write
	tm.docker.answer = func(args []string) (string, int, bool) {
		if len(args) < 3 || args[0] != "run" || args[len(args)-1] != awsApplyWriteArg {
			return "", 0, false
		}
		tm.docker.mu.Lock()
		in := tm.docker.stdin[len(tm.docker.stdin)-1]
		tm.docker.mu.Unlock()
		var w awsiac.Write
		if err := json.Unmarshal([]byte(in), &w); err != nil {
			return "bad stdin", 1, true
		}
		got = append(got, w)
		b, _ := json.Marshal(reply(w))
		return "Creating ...\n" + AWSApplyMarker + string(b) + "\n", 0, true
	}
	return &got
}

func TestAWSWriteRefusals(t *testing.T) {
	tm, run, _ := awsReadSetup(t, 1<<20)
	ctx := context.Background()
	before := len(tm.docker.calls)
	cases := map[string]struct {
		req  AWSWriteRequest
		code string
	}{
		"session caller":    {AWSWriteRequest{Op: awsiac.WriteS3Put, Bucket: "uploads", Key: "a"}, CodeNeedsOperator},
		"unknown op":        {AWSWriteRequest{Op: "s3_copy", Operator: true}, ""},
		"bad bucket":        {AWSWriteRequest{Op: awsiac.WriteS3Put, Bucket: "_x", Key: "a", Operator: true}, ""},
		"dot segment":       {AWSWriteRequest{Op: awsiac.WriteS3Delete, Bucket: "uploads", Key: "a/../b", Operator: true}, ""},
		"control char":      {AWSWriteRequest{Op: awsiac.WriteS3Put, Bucket: "uploads", Key: "a\nb", Operator: true}, ""},
		"too big":           {AWSWriteRequest{Op: awsiac.WriteS3Put, Bucket: "uploads", Key: "a", Data: make([]byte, MaxAWSWriteData+1), Operator: true}, ""},
		"bad table":         {AWSWriteRequest{Op: awsiac.WriteDDBPut, Table: "a", Item: map[string]any{"pk": "x"}, Operator: true}, ""},
		"no item":           {AWSWriteRequest{Op: awsiac.WriteDDBPut, Table: "orders", Operator: true}, ""},
		"item without key":  {AWSWriteRequest{Op: awsiac.WriteDDBPut, Table: "orders", Item: map[string]any{"pk": "x"}, Operator: true}, ""},
		"key partial":       {AWSWriteRequest{Op: awsiac.WriteDDBDelete, Table: "orders", ItemKey: map[string]any{"pk": "x"}, Operator: true}, ""},
		"key wrong type":    {AWSWriteRequest{Op: awsiac.WriteDDBDelete, Table: "orders", ItemKey: map[string]any{"pk": "x", "sk": "seven"}, Operator: true}, ""},
		"bad queue":         {AWSWriteRequest{Op: awsiac.WriteSQSSend, Queue: "a b", Body: "x", Operator: true}, ""},
		"empty body":        {AWSWriteRequest{Op: awsiac.WriteSQSSend, Queue: "events", Operator: true}, ""},
		"bad attribute":     {AWSWriteRequest{Op: awsiac.WriteSQSSend, Queue: "events", Body: "x", Attributes: map[string]string{"AWS.x": "1"}, Operator: true}, ""},
		"bad group":         {AWSWriteRequest{Op: awsiac.WriteSQSSend, Queue: "events", Body: "x", GroupID: "a b", Operator: true}, ""},
		"other aws service": {AWSWriteRequest{Op: awsiac.WriteReset, Service: "db", Operator: true}, ""},
	}
	for name, c := range cases {
		c.req.Run = run.ID
		_, err := tm.AWSWrite(ctx, c.req)
		var he *Error
		switch {
		case c.code != "":
			if !errors.As(err, &he) || he.Code != c.code {
				t.Errorf("%s: %v, want %s", name, err, c.code)
			}
		case !errors.Is(err, ErrParams):
			t.Errorf("%s: %v, want invalid params", name, err)
		}
	}
	if _, err := tm.AWSWrite(ctx, AWSWriteRequest{Run: "nope", Op: awsiac.WriteReset, Operator: true}); !errors.As(err, new(*Error)) {
		t.Errorf("no run: %v", err)
	}
	if n := tm.docker.called("run --rm -T --no-deps aws-emu-apply"); n != 0 || len(tm.docker.calls) != before {
		t.Fatalf("a refused write ran the applier: %v", tm.docker.calls[before:])
	}
	if tm.rec.last(domain.EventHarnessAWSWrite) != nil {
		t.Fatal("a refused write was recorded")
	}
}

func TestAWSWriteOps(t *testing.T) {
	tm, run, _ := awsReadSetup(t, 1<<20)
	ctx := context.Background()
	found := true
	writes := answerWrites(tm, func(w awsiac.Write) awsApplyReply {
		r := &awsiac.WriteResult{Op: w.Op, Status: 200}
		switch w.Op {
		case awsiac.WriteS3Put:
			r.ETag, r.VersionID = "e9", "v3"
		case awsiac.WriteDDBDelete:
			r.Found = &found
		case awsiac.WriteSQSSend:
			r.MessageID = "m-1"
		}
		return awsApplyReply{Write: r}
	})
	const secret = "top-secret-value"
	put, err := tm.AWSWrite(ctx, AWSWriteRequest{Run: run.ID, Op: awsiac.WriteS3Put, Bucket: "uploads", Key: "in/" + secret + ".txt",
		ContentType: "text/plain", Data: []byte(secret), Operator: true, Actor: domain.EventActorUser})
	if err != nil || put.ETag != "e9" || put.VersionID != "v3" || put.Bytes != len(secret) || put.Service != "aws" || put.Bucket != "uploads" {
		t.Fatalf("put %+v %v", put, err)
	}
	w := (*writes)[0]
	if w.Op != awsiac.WriteS3Put || string(w.Data) != secret || w.Key != "in/"+secret+".txt" || w.Region == "" || !strings.HasSuffix(w.Endpoint, ":4566") || w.Plan != nil {
		t.Fatalf("applier got %+v", w)
	}
	if tm.docker.called("run --rm -T --no-deps aws-emu-apply harness aws-apply --write") != 1 {
		t.Fatalf("calls %v", tm.docker.calls)
	}
	row := tm.rec.last(domain.EventHarnessAWSWrite)
	if row["op"] != awsiac.WriteS3Put || row["bucket"] != "uploads" || row["key_sha256"] != sha256Hex([]byte("in/"+secret+".txt")) ||
		row["payload_sha256"] != sha256Hex([]byte(secret)) || row["run"] != run.ID {
		t.Fatalf("ledger %v", row)
	}

	// Keys are typed from the table's schema: sk is a number.
	if _, err := tm.AWSWrite(ctx, AWSWriteRequest{Run: run.ID, Op: awsiac.WriteDDBPut, Table: "orders",
		Item: map[string]any{"pk": "o-9", "sk": "7", "note": secret, "n": json.Number("1.5")}, Operator: true}); err != nil {
		t.Fatal(err)
	}
	w = (*writes)[1]
	item, _ := json.Marshal(w.Item)
	if string(item) != `{"n":{"N":"1.5"},"note":{"S":"`+secret+`"},"pk":{"S":"o-9"},"sk":{"N":"7"}}` || w.ItemKey != nil {
		t.Fatalf("item %s key %v", item, w.ItemKey)
	}
	row = tm.rec.last(domain.EventHarnessAWSWrite)
	if row["table"] != "orders" || row["key_sha256"] != sha256Hex([]byte(`{"pk":{"S":"o-9"},"sk":{"N":"7"}}`)) || row["payload_sha256"] != sha256Hex(item) {
		t.Fatalf("ledger %v", row)
	}
	del, err := tm.AWSWrite(ctx, AWSWriteRequest{Run: run.ID, Op: awsiac.WriteDDBDelete, Table: "orders",
		ItemKey: map[string]any{"pk": "o-9", "sk": json.Number("7")}, Operator: true})
	if err != nil || del.Found == nil || !*del.Found {
		t.Fatalf("delete %+v %v", del, err)
	}
	if k, _ := json.Marshal((*writes)[2].ItemKey); string(k) != `{"pk":{"S":"o-9"},"sk":{"N":"7"}}` {
		t.Fatalf("delete key %s", k)
	}
	msg, err := tm.AWSWrite(ctx, AWSWriteRequest{Run: run.ID, Op: awsiac.WriteSQSSend, Queue: "events", Body: secret,
		Attributes: map[string]string{"kind": "order"}, Operator: true})
	if err != nil || msg.MessageID != "m-1" || msg.Queue != "events" {
		t.Fatalf("send %+v %v", msg, err)
	}
	// No ledger row holds a key or value.
	for _, r := range tm.rec.rows {
		if r.kind != domain.EventHarnessAWSWrite {
			continue
		}
		b, _ := json.Marshal(r.payload)
		if strings.Contains(string(b), secret) || strings.Contains(string(b), "o-9") {
			t.Fatalf("ledger holds a value: %s", b)
		}
	}
}

// TestAWSWriteEmulatorRefusal: the emulator's 4xx is the caller's mistake
// and nothing is recorded.
func TestAWSWriteEmulatorRefusal(t *testing.T) {
	tm, run, _ := awsReadSetup(t, 1<<20)
	answerWrites(tm, func(w awsiac.Write) awsApplyReply {
		f := awsiac.StepResult{N: 1, Service: "s3", Operation: "DeleteObject", Status: 404, Code: "NoSuchBucket", Message: "no bucket"}
		return awsApplyReply{Write: &awsiac.WriteResult{Op: w.Op, Status: 404, Failed: &f}}
	})
	_, err := tm.AWSWrite(context.Background(), AWSWriteRequest{Run: run.ID, Op: awsiac.WriteS3Delete, Bucket: "gone", Key: "a", Operator: true})
	if !errors.Is(err, ErrParams) || !strings.Contains(err.Error(), "NoSuchBucket") {
		t.Fatalf("err %v", err)
	}
	if tm.rec.last(domain.EventHarnessAWSWrite) != nil {
		t.Fatal("a refused write was recorded")
	}
}

// TestAWSWriteReset resets with the run's plan: the plan goes to the
// applier with the reset, a new stream ARN reaches the manifest and the
// compose file, and the reply names the changed env variable.
func TestAWSWriteReset(t *testing.T) {
	useDeclared(t)
	repo := awsRepo(t, awsPlanFile, nil)
	tm := newTestManager(t, func(o *Options) { o.Images = &recordingVetter{} })
	stream := func(n string) string { return "arn:aws:dynamodb:eu-west-1:000000000000:table/x/stream/" + n }
	tm.docker.answer = func(args []string) (string, int, bool) {
		if len(args) > 4 && args[0] == "run" && args[len(args)-1] == AWSApplyService("aws") {
			b, _ := json.Marshal(awsApplyReply{Result: &awsiac.ApplyResult{Learned: map[string]map[string]string{"orders.OrdersTable": {awsiac.AttrStreamARN: stream("1")}}}})
			return AWSApplyMarker + string(b) + "\n", 0, true
		}
		return "", 0, false
	}
	run := tm.upReady(t, repo, "seeded")
	calls := answerWrites(tm, func(w awsiac.Write) awsApplyReply {
		steps := make([]awsiac.StepResult, len(w.Plan.Steps))
		return awsApplyReply{Write: &awsiac.WriteResult{Op: w.Op, Status: 200, Apply: &awsiac.ApplyResult{
			Steps: steps, Learned: map[string]map[string]string{"orders.OrdersTable": {awsiac.AttrStreamARN: stream("2")}}}}}
	})
	res, err := tm.AWSWrite(context.Background(), AWSWriteRequest{Run: run.ID, Op: awsiac.WriteReset, Operator: true, Actor: domain.EventActorUser})
	if err != nil {
		t.Fatal(err)
	}
	w := (*calls)[0]
	if w.Op != awsiac.WriteReset || w.Plan == nil || len(w.Plan.Steps) == 0 || w.Plan.Seed == nil {
		t.Fatalf("applier got %+v", w)
	}
	if res.Resources != 6 || res.Calls != len(w.Plan.Steps) || len(res.EnvChanged) != 1 || res.EnvChanged[0] != "ORDERS_STREAM" || !strings.Contains(res.Note, "running app keeps") {
		t.Fatalf("reset %+v", res)
	}
	sandbox := filepath.Join(tm.dir, "s1", run.ID, "sandbox")
	if b, _ := os.ReadFile(filepath.Join(sandbox, AWSResourcesFile)); !strings.Contains(string(b), stream("2")) {
		t.Fatalf("manifest %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(sandbox, "compose.yaml")); !strings.Contains(string(b), "ORDERS_STREAM: "+stream("2")) {
		t.Fatal("compose keeps the old stream")
	}
	row := tm.rec.last(domain.EventHarnessAWSWrite)
	if row["op"] != awsiac.WriteReset || row["resources"] != 6 || row["calls"] != len(w.Plan.Steps) {
		t.Fatalf("ledger %v", row)
	}
}
