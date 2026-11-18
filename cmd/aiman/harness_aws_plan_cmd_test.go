package main

import (
	"strings"
	"testing"
)

func TestHarnessCmdAWSPlan(t *testing.T) {
	f := &fakeHarness{t: t, answers: map[string][]any{
		"harness.aws_plan": {`{"type":"harness_aws_plan","session":"s1","sha":"0123456789abcdef","plans":[{"service":"aws","scenario":"upload","region":"eu-west-1","endpoint":"http://aws:4566",` +
			`"resources":[{"stack":"orders","logical_id":"UploadQueue","type":"AWS::SQS::Queue","kind":"queue","physical":"orders-UploadQueue-abc","generated":true,"attributes":{}}],` +
			`"outputs":{"orders":{"UploadBucketName":"b1"}},"env":{"QUEUE_URL":"http://aws:4566/000000000000/orders-UploadQueue-abc"},` +
			`"steps":[{"n":1,"resource":"orders.UploadQueue","service":"sqs","operation":"CreateQueue"}],` +
			`"warnings":[{"stack":"orders","resource":"Bucket","attribute":"LambdaConfigurations","message":"not emulated"}],"notes":[]}]}`},
	}}
	out, _, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "aws", "plan", "upload")
	if err != nil {
		t.Fatal(err)
	}
	if c := f.calls[0]; c.method != "harness.aws_plan" || c.params["session"] != "s1" || c.params["scenario"] != "upload" {
		t.Fatalf("call %+v", c)
	}
	for _, want := range []string{
		"aws service aws (eu-west-1) at 0123456789ab, scenario upload", "orders-UploadQueue-abc (generated)",
		"output orders.outputs.UploadBucketName = b1", "env QUEUE_URL=http://aws:4566/", "1 calls",
		"warning: orders.Bucket LambdaConfigurations: not emulated",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
	if _, _, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "aws", "plan", "a", "b"); err == nil {
		t.Fatal("two scenarios accepted")
	}
}
