package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/edge"
)

// Capture answers one flow whose header the edge did not redact, so the test
// sees serve redact it again.
func (harnessEdge) Capture(_ context.Context, _, _ string, after int64, _ int) ([]edge.Flow, edge.CaptureInfo, error) {
	if after >= 1 {
		return []edge.Flow{}, edge.CaptureInfo{Network: "offline", Total: 1, Next: after}, nil
	}
	return []edge.Flow{{
		ID: 1, Method: "GET", URL: "https://api.example.com/v1?token=abcdef123456", Outcome: edge.OutcomeOffline, Status: 502,
		RequestHeaders: map[string][]string{"Authorization": {"Bearer live-token-123456"}},
	}}, edge.CaptureInfo{Network: "offline", Total: 1, Next: 1}, nil
}

func (harnessEdge) CA(context.Context, string, string) ([]byte, error) {
	return []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"), nil
}

func TestHarnessCapture(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	run := harnessUp(t, srv, "s1", map[string]any{"session": "s1", "scenario": "plain", "wait": true})
	resp := rpc(t, srv, "s1", "harness.capture", map[string]any{"run": run.ID})
	if resp.Error != nil {
		t.Fatalf("capture: %+v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	if strings.Contains(string(raw), "live-token-123456") || strings.Contains(string(raw), "abcdef123456") {
		t.Fatalf("capture reply keeps a secret: %s", raw)
	}
	var got struct {
		Type     string      `json:"type"`
		Network  string      `json:"network"`
		Flows    []edge.Flow `json:"flows"`
		Next     int64       `json:"next"`
		Redacted int         `json:"redacted"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "harness_capture" || got.Network != "offline" || len(got.Flows) != 1 || got.Next != 1 || got.Redacted < 2 {
		t.Fatalf("capture = %s", raw)
	}
	wantHarnessCode(t, "negative after", rpc(t, srv, "s1", "harness.capture", map[string]any{"run": run.ID, "after_id": -1}), CodeInvalidParams)
	// Another session's run is out of scope.
	wantHarnessCode(t, "out of scope", rpc(t, srv, "s2", "harness.capture", map[string]any{"run": run.ID}), CodeSessionScope)
	// After harness.down the saved flows still answer.
	if r := rpc(t, srv, "", "harness.down", map[string]any{"run": run.ID}); r.Error != nil {
		t.Fatalf("down: %+v", r.Error)
	}
	resp = rpc(t, srv, "", "harness.capture", map[string]any{"run": run.ID})
	if resp.Error != nil {
		t.Fatalf("capture after down: %+v", resp.Error)
	}
	if raw, _ := json.Marshal(resp.Result); !strings.Contains(string(raw), `"state":"`+harness.StateStopped+`"`) || !strings.Contains(string(raw), "api.example.com") {
		t.Fatalf("capture after down = %s", raw)
	}
}

// The reply's redacted counts the edge's replacements too: a flow the edge
// already redacted finds nothing new in serve's pass.
func TestRedactFlowsCountsEdgeRedactions(t *testing.T) {
	flows := []edge.Flow{
		{ID: 1, Redacted: 1, RequestHeaders: map[string][]string{"Authorization": {"[redacted]"}}},
		{ID: 2, RequestHeaders: map[string][]string{"Authorization": {"Bearer live-token-123456"}}},
	}
	if got := redactFlows(flows); got != 2 {
		t.Fatalf("redactFlows = %d, want 2", got)
	}
	if flows[0].Redacted != 1 || flows[1].Redacted != 1 {
		t.Fatalf("per-flow redacted = %d, %d, want 1, 1", flows[0].Redacted, flows[1].Redacted)
	}
	if v := flows[1].RequestHeaders["Authorization"][0]; strings.Contains(v, "live-token") {
		t.Fatalf("authorization kept: %q", v)
	}
}
