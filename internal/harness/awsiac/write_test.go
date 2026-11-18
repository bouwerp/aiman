package awsiac

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type seenCall struct {
	method, uri, target, agent, auth, hash string
	body                                   string
}

// fakeEmu answers the operator writes as MiniStack would and records them.
func fakeEmu(t *testing.T) (*httptest.Server, func() []seenCall) {
	t.Helper()
	var mu sync.Mutex
	var seen []seenCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		target := r.Header.Get("X-Amz-Target")
		mu.Lock()
		seen = append(seen, seenCall{r.Method, r.URL.RequestURI(), target, r.UserAgent(), r.Header.Get("Authorization"), r.Header.Get("X-Amz-Content-Sha256"), string(b)})
		mu.Unlock()
		switch {
		case r.URL.Path == ResetPath:
			_, _ = io.WriteString(w, `{"reset":"ok"}`)
		case r.Method == "PUT" && r.URL.Path == "/gone/a":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<Error><Code>NoSuchBucket</Code><Message>no such bucket</Message></Error>`)
		case r.Method == "PUT":
			w.Header().Set("ETag", `"abc"`)
			w.Header().Set("X-Amz-Version-Id", "v1")
		case r.Method == "DELETE":
			w.Header().Set("X-Amz-Delete-Marker", "true")
			w.Header().Set("X-Amz-Version-Id", "dm1")
			w.WriteHeader(http.StatusNoContent)
		case target == dynamoTarget+"DeleteItem":
			_, _ = io.WriteString(w, `{"Attributes":{"pk":{"S":"a"}}}`)
		case target == sqsTarget+"GetQueueUrl":
			_, _ = io.WriteString(w, `{"QueueUrl":"http://aws:4566/000000000000/jobs.fifo"}`)
		case target == sqsTarget+"SendMessage":
			_, _ = io.WriteString(w, `{"MessageId":"m-1"}`)
		case target == sqsTarget+"CreateQueue":
			_, _ = io.WriteString(w, `{"QueueUrl":"http://aws:4566/000000000000/q"}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []seenCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenCall(nil), seen...)
	}
}

func TestApplierWrite(t *testing.T) {
	srv, seen := fakeEmu(t)
	ctx := context.Background()
	a := &Applier{Key: "test", Secret: "test"}
	base := Write{Endpoint: srv.URL, Region: "eu-west-1"}

	w := base
	w.Op, w.Bucket, w.Key, w.Data, w.ContentType = WriteS3Put, "uploads", "in/a b.txt", []byte("hello"), "text/plain"
	res, err := a.Write(ctx, &w)
	if err != nil || res.ETag != "abc" || res.VersionID != "v1" || res.Status != 200 {
		t.Fatalf("put %+v %v", res, err)
	}
	c := seen()[0]
	if c.method != "PUT" || c.uri != "/uploads/in/a%20b.txt" || c.body != "hello" || c.hash != hashHex([]byte("hello")) ||
		c.agent != WriteAgent || !strings.HasPrefix(c.auth, "AWS4-HMAC-SHA256 Credential=test/") || !strings.Contains(c.auth, "/eu-west-1/s3/") {
		t.Fatalf("put sent %+v", c)
	}

	w = base
	w.Op, w.Bucket, w.Key, w.Version = WriteS3Delete, "uploads", "a", "v1"
	if res, err = a.Write(ctx, &w); err != nil || !res.DeleteMarker || res.VersionID != "dm1" {
		t.Fatalf("delete %+v %v", res, err)
	}
	if c = seen()[1]; c.method != "DELETE" || c.uri != "/uploads/a?versionId=v1" {
		t.Fatalf("delete sent %+v", c)
	}

	w = base
	w.Op, w.Table, w.Item = WriteDDBPut, "orders", map[string]any{"pk": map[string]any{"S": "a"}}
	if _, err = a.Write(ctx, &w); err != nil {
		t.Fatal(err)
	}
	if c = seen()[2]; c.target != dynamoTarget+"PutItem" || c.body != `{"Item":{"pk":{"S":"a"}},"TableName":"orders"}` {
		t.Fatalf("put item sent %+v", c)
	}
	w = base
	w.Op, w.Table, w.ItemKey = WriteDDBDelete, "orders", map[string]any{"pk": map[string]any{"S": "a"}}
	if res, err = a.Write(ctx, &w); err != nil || res.Found == nil || !*res.Found {
		t.Fatalf("delete item %+v %v", res, err)
	}

	w = base
	w.Op, w.Queue, w.Body = WriteSQSSend, "jobs.fifo", "job one"
	if res, err = a.Write(ctx, &w); err != nil || res.MessageID != "m-1" {
		t.Fatalf("send %+v %v", res, err)
	}
	all := seen()
	if all[4].target != sqsTarget+"GetQueueUrl" || all[5].target != sqsTarget+"SendMessage" {
		t.Fatalf("send calls %+v", all[4:])
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(all[5].body), &sent)
	if sent["QueueUrl"] != "http://aws:4566/000000000000/jobs.fifo" || sent["MessageGroupId"] != "operator" || sent["MessageDeduplicationId"] == "" {
		t.Fatalf("send body %v", sent)
	}

	// A refusal names the operation and its code.
	w = base
	w.Op, w.Bucket, w.Key, w.Data = WriteS3Put, "gone", "a", []byte("x")
	res, err = a.Write(ctx, &w)
	if err == nil || res.Failed == nil || res.Failed.Code != "NoSuchBucket" || res.Failed.Status != 404 || res.Failed.Operation != "PutObject" {
		t.Fatalf("refusal %+v %v", res, err)
	}
}

// TestApplierReset posts the reset unsigned, then applies the plan with the
// applier's own User-Agent.
func TestApplierReset(t *testing.T) {
	srv, seen := fakeEmu(t)
	p := New("aws", "eu-west-1", srv.URL, []string{"sqs"})
	if err := p.Add(&Resource{Stack: "s", LogicalID: "Q", Type: "AWS::SQS::Queue", Kind: KindQueue, Queue: &Queue{}}); err != nil {
		t.Fatal(err)
	}
	if err := p.Build(); err != nil {
		t.Fatal(err)
	}
	a := &Applier{Key: "test", Secret: "test"}
	res, err := a.Write(context.Background(), &Write{Op: WriteReset, Endpoint: srv.URL, Region: "eu-west-1", Plan: p})
	if err != nil || res.Apply == nil || len(res.Apply.Steps) == 0 {
		t.Fatalf("reset %+v %v", res, err)
	}
	all := seen()
	if all[0].method != "POST" || all[0].uri != ResetPath || all[0].auth != "" || all[0].agent != WriteAgent {
		t.Fatalf("reset sent %+v", all[0])
	}
	if all[1].target != sqsTarget+"CreateQueue" || all[1].agent != UserAgent {
		t.Fatalf("apply sent %+v", all[1])
	}
}
