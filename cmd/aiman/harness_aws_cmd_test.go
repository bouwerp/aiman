package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHarnessAWSCmdS3(t *testing.T) {
	f := &fakeHarness{t: t, answers: map[string][]any{"harness.aws": {
		`{"type":"harness_aws","op":"s3_objects","objects":[{"key":"in/a.txt","size":5,"modified":"2026-11-18"}],"prefixes":["in/sub/"],"next_token":"t2"}`,
	}}}
	out, errOut, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "aws", "s3", "ls", "uploads/in/", "--limit", "10")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"op": "s3_objects", "session": "s1", "bucket": "uploads", "prefix": "in/", "limit": 10}
	if !reflect.DeepEqual(f.calls[0].params, want) {
		t.Fatalf("params %v", f.calls[0].params)
	}
	if !strings.Contains(out, "PRE") || !strings.Contains(out, "in/a.txt") || !strings.Contains(errOut, "--token t2") {
		t.Fatalf("out %q err %q", out, errOut)
	}
}

func TestHarnessAWSCmdGetPages(t *testing.T) {
	f := &fakeHarness{t: t, answers: map[string][]any{"harness.aws": {
		`{"op":"s3_download","download":{"size":10,"offset":0,"next":5,"eof":false,"max":67108864,"data":"aGVsbG8="}}`,
		`{"op":"s3_download","download":{"size":10,"offset":5,"next":10,"eof":true,"max":67108864,"data":"d29ybGQ="}}`,
	}}}
	dst := filepath.Join(t.TempDir(), "o.bin")
	if _, _, err := runHarnessTest(t, f, nil, "aws", "s3", "get", "uploads/a b.txt", "--run", "r1", "--out", dst); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dst)
	if string(b) != "helloworld" || len(f.calls) != 2 || f.calls[1].params["offset"] != int64(5) || f.calls[0].params["key"] != "a b.txt" || f.calls[0].params["run"] != "r1" {
		t.Fatalf("file %q calls %+v", b, f.calls)
	}
}

func TestHarnessAWSCmdDDB(t *testing.T) {
	f := &fakeHarness{t: t, answers: map[string][]any{"harness.aws": {
		`{"op":"ddb_query","items":[{"pk":"o-1"}],"last_key":"abc","redacted_attributes":["password"],"redacted":1}`,
		`{"op":"ddb_get","found":false}`,
	}}}
	out, errOut, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "aws", "ddb", "query", "orders", "--pk", "pk=o-1", "--sk", "between 5 9", "--desc", "--index", "by-status")
	if err != nil {
		t.Fatal(err)
	}
	p := f.calls[0].params
	if p["table"] != "orders" || p["index"] != "by-status" || p["forward"] != false ||
		!reflect.DeepEqual(p["pk"], map[string]any{"name": "pk", "value": "o-1"}) ||
		!reflect.DeepEqual(p["sk"], map[string]any{"op": "between", "values": []any{float64(5), float64(9)}}) {
		t.Fatalf("params %v", p)
	}
	if !strings.Contains(out, `{"pk":"o-1"}`) || !strings.Contains(errOut, "--start abc") || !strings.Contains(errOut, "password") {
		t.Fatalf("out %q err %q", out, errOut)
	}
	if _, errOut, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "aws", "ddb", "get", "orders", `{"pk":"o-9","sk":7}`); err != nil || !strings.Contains(errOut, "no such item") {
		t.Fatalf("get: %v %q", err, errOut)
	}
	for _, bad := range [][]string{
		{"aws"}, {"aws", "nope"}, {"aws", "ddb", "query", "orders"}, {"aws", "ddb", "get", "orders", "x"},
		{"aws", "s3", "get"}, {"aws", "s3", "ls", "--bogus"},
	} {
		if _, _, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, bad...); !errors.Is(err, errUsage) {
			t.Errorf("%v: %v", bad, err)
		}
	}
	if _, _, err := runHarnessTest(t, f, nil, "aws", "ddb", "tables"); !errors.Is(err, errUsage) {
		t.Errorf("no session: %v", err)
	}
}
