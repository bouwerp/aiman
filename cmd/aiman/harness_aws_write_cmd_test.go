package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHarnessAWSWriteCmd(t *testing.T) {
	f := &fakeHarness{t: t, answers: map[string][]any{"harness.aws_write": {
		`{"type":"harness_aws_write","op":"s3_put","service":"aws","bucket":"uploads","etag":"e1","bytes":5}`,
		`{"type":"harness_aws_write","op":"ddb_put","table":"orders"}`,
		`{"type":"harness_aws_write","op":"ddb_delete","table":"orders","found":false}`,
		`{"type":"harness_aws_write","op":"sqs_send","queue":"jobs","message_id":"m-1"}`,
		`{"type":"harness_aws_write","op":"reset","service":"aws","resources":6,"calls":14}`,
	}}}
	src := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := runHarnessTest(t, f, nil, "aws", "write", "s3-put", "uploads/in/a.txt", "--file", src, "--run", "r1", "--content-type", "text/plain")
	if err != nil || !strings.Contains(out, "put 5 bytes in uploads (etag e1)") {
		t.Fatalf("%v %q", err, out)
	}
	want := map[string]any{"op": "s3_put", "run": "r1", "bucket": "uploads", "key": "in/a.txt", "data": "aGVsbG8=", "content_type": "text/plain"}
	if !reflect.DeepEqual(f.calls[0].params, want) {
		t.Fatalf("params %v", f.calls[0].params)
	}
	if _, _, err = runHarnessTest(t, f, nil, "aws", "write", "ddb-put", "orders", `{"pk":"o-1","n":1.50}`, "--run", "r1"); err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(f.calls[1].params["item"]); string(b) != `{"n":1.50,"pk":"o-1"}` {
		t.Fatalf("item %s", b)
	}
	out, _, err = runHarnessTest(t, f, nil, "aws", "write", "ddb_delete", "orders", `{"pk":"o-1"}`, "--run", "r1")
	if err != nil || !strings.Contains(out, "no such item in orders") {
		t.Fatalf("%v %q", err, out)
	}
	out, _, err = runHarnessTest(t, f, nil, "aws", "write", "sqs-send", "jobs", "--text", "go", "--attr", "kind=order", "--group", "g", "--run", "r1")
	if err != nil || !strings.Contains(out, "sent message m-1 to jobs") {
		t.Fatalf("%v %q", err, out)
	}
	p := f.calls[3].params
	if p["body"] != "go" || p["group_id"] != "g" || !reflect.DeepEqual(p["attributes"], map[string]string{"kind": "order"}) {
		t.Fatalf("params %v", p)
	}
	out, _, err = runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "aws", "reset")
	if err != nil || !strings.Contains(out, "reset aws: 6 resources, 14 calls") || f.calls[4].params["op"] != "reset" || f.calls[4].params["session"] != "s1" {
		t.Fatalf("%v %q %v", err, out, f.calls[4].params)
	}
}

func TestHarnessAWSWriteCmdUsage(t *testing.T) {
	f := &fakeHarness{t: t}
	for _, args := range [][]string{
		{"aws", "write"},
		{"aws", "write", "s3-put", "uploads/a", "--run", "r1"},
		{"aws", "write", "s3-put", "uploads/a", "--text", "x", "--file", "y", "--run", "r1"},
		{"aws", "write", "ddb-put", "orders", "[1]", "--run", "r1"},
		{"aws", "write", "s3-copy", "a", "--run", "r1"},
		{"aws", "write", "reset"},
		{"aws", "write", "reset", "extra", "--run", "r1"},
	} {
		if _, _, err := runHarnessTest(t, f, nil, args...); !errors.Is(err, errUsage) {
			t.Errorf("%v: %v, want usage", args, err)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls %v", f.calls)
	}
}
