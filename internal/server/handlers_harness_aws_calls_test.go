package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/edge"
)

// AWSCalls answers one call whose params and resource the edge did not
// redact, so the test sees serve redact them again.
func (harnessEdge) AWSCalls(_ context.Context, _, _ string, after int64, _ int) ([]edge.AWSCall, edge.AWSCallsInfo, error) {
	if after >= 1 {
		return []edge.AWSCall{}, edge.AWSCallsInfo{Service: "aws", Total: 1, Next: after}, nil
	}
	return []edge.AWSCall{{
		ID: 1, Client: "app", Protocol: edge.AWSProtocolJSON, Service: "dynamodb", Operation: "PutItem", Method: "POST",
		Resource: edge.AWSResource{Table: "orders"}, Status: 400, ErrorCode: "ConditionalCheckFailedException",
		Params: `{"TableName":"orders","api_token":"live-token-123456"}`, AccessKey: "test",
	}}, edge.AWSCallsInfo{Service: "aws", Total: 1, Next: 1}, nil
}

func TestHarnessAWSCalls(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{aws: true})
	plain := harnessUp(t, srv, "s1", map[string]any{"session": "s1", "scenario": "plain", "wait": true})
	wantHarnessCode(t, "no aws service", rpc(t, srv, "s1", "harness.aws_calls", map[string]any{"run": plain.ID}), CodeInvalidParams)
	if r := rpc(t, srv, "", "harness.down", map[string]any{"run": plain.ID}); r.Error != nil {
		t.Fatalf("down: %+v", r.Error)
	}

	run := harnessUp(t, srv, "s1", map[string]any{"session": "s1", "scenario": "aws", "wait": true})
	resp := rpc(t, srv, "s1", "harness.aws_calls", map[string]any{"run": run.ID})
	if resp.Error != nil {
		t.Fatalf("aws_calls: %+v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	if strings.Contains(string(raw), "live-token-123456") {
		t.Fatalf("reply keeps a secret: %s", raw)
	}
	var got struct {
		Type     string         `json:"type"`
		Service  string         `json:"service"`
		Calls    []edge.AWSCall `json:"calls"`
		Next     int64          `json:"next"`
		Total    int64          `json:"total"`
		Redacted int            `json:"redacted"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "harness_aws_calls" || got.Service != "aws" || len(got.Calls) != 1 || got.Next != 1 || got.Total != 1 || got.Redacted < 1 ||
		got.Calls[0].Operation != "PutItem" || got.Calls[0].ErrorCode != "ConditionalCheckFailedException" {
		t.Fatalf("aws_calls = %s", raw)
	}
	wantHarnessCode(t, "negative limit", rpc(t, srv, "s1", "harness.aws_calls", map[string]any{"run": run.ID, "limit": -1}), CodeInvalidParams)
	wantHarnessCode(t, "out of scope", rpc(t, srv, "s2", "harness.aws_calls", map[string]any{"run": run.ID}), CodeSessionScope)
	// After harness.down the saved calls still answer.
	if r := rpc(t, srv, "", "harness.down", map[string]any{"run": run.ID}); r.Error != nil {
		t.Fatalf("down: %+v", r.Error)
	}
	resp = rpc(t, srv, "", "harness.aws_calls", map[string]any{"run": run.ID})
	if resp.Error != nil {
		t.Fatalf("aws_calls after down: %+v", resp.Error)
	}
	if raw, _ := json.Marshal(resp.Result); !strings.Contains(string(raw), `"state":"`+harness.StateStopped+`"`) || !strings.Contains(string(raw), "PutItem") {
		t.Fatalf("aws_calls after down = %s", raw)
	}
}
