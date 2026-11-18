package main

import (
	"strings"
	"testing"
)

func TestHarnessCmdAWSCalls(t *testing.T) {
	f := &fakeHarness{t: t, answers: map[string][]any{
		"harness.aws_calls": {`{"type":"harness_aws_calls","run":"r1","service":"aws","state":"ready","total":3,"dropped":0,"next":3,"more":true,` +
			`"calls":[{"id":2,"start":"2026-11-18T12:00:00Z","duration_ms":4,"client":"app","protocol":"rest-xml","service":"s3","operation":"PutObject","method":"PUT","resource":{"bucket":"uploads","key":"a/b.txt"},"status":200,"request_bytes":5,"response_bytes":0},` +
			`{"id":3,"start":"2026-11-18T12:00:01Z","duration_ms":2,"client":"app","protocol":"json","service":"dynamodb","operation":"PutItem","method":"POST","resource":{"table":"orders"},"status":400,"error_code":"ConditionalCheckFailedException","presigned":false,"request_bytes":90,"response_bytes":120}]}`},
	}}
	out, _, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "aws", "calls", "--after", "1", "--limit", "2")
	if err != nil {
		t.Fatal(err)
	}
	p := f.calls[0].params
	if f.calls[0].method != "harness.aws_calls" || p["session"] != "s1" || p["after_id"] != int64(1) || p["limit"] != 2 {
		t.Fatalf("call %+v", f.calls[0])
	}
	for _, want := range []string{"run r1, aws service aws, 3 calls", "PutObject", "uploads/a/b.txt", "PutItem", "400", "ConditionalCheckFailedException", "--after 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
	if _, _, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "aws", "nope"); err == nil {
		t.Fatal("unknown aws command accepted")
	}
}
