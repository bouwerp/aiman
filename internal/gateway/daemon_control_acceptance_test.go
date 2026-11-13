package gateway

import (
	"encoding/json"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// Acceptance tests for daemon-control piece B (refusal data, allowlist,
// gateway.restart_serve). Do not edit.

type refusalBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    *struct {
			Method string `json:"method"`
			Needs  string `json:"needs"`
		} `json:"data"`
	} `json:"error"`
}

func decodeRefusal(t *testing.T, body []byte) refusalBody {
	t.Helper()
	var r refusalBody
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("refusal is not JSON: %s", body)
	}
	return r
}

func TestDeviceRefusalData(t *testing.T) {
	rig := newDeviceRig(t, nil)
	_, plain := rig.device(t, false)

	rec := rig.do(http.MethodPost, "/v1/rpc", plain, `{"id":"1","method":"session.create","params":{}}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	r := decodeRefusal(t, rec.Body.Bytes())
	if r.Error.Code != "forbidden" || r.Error.Data == nil || r.Error.Data.Needs != "allow_destructive" || r.Error.Data.Method != "session.create" {
		t.Fatalf("destructive refusal = %+v", r.Error)
	}

	rec = rig.do(http.MethodPost, "/v1/rpc", plain, `{"id":"2","method":"limits.get","params":{}}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	r = decodeRefusal(t, rec.Body.Bytes())
	if r.Error.Code != "forbidden" || (r.Error.Data != nil && r.Error.Data.Needs != "") {
		t.Fatalf("off-allowlist refusal must carry no needs: %+v", r.Error)
	}
	if rig.forwards.Load() != 0 {
		t.Fatal("a refused call was forwarded")
	}
}

func TestAllowlistDaemonControlMethods(t *testing.T) {
	if !slices.Contains(deviceReadMethods, "host.daemons") {
		t.Error("host.daemons should be a device read method")
	}
	for _, m := range []string{"host.logs", "host.restart", "gateway.restart_serve"} {
		if !slices.Contains(deviceDestructiveMethods, m) {
			t.Errorf("%s should be destructive", m)
		}
		if deviceMayCall(m, false) || !deviceMayCall(m, true) {
			t.Errorf("deviceMayCall(%s) wrong", m)
		}
	}
}

func TestRestartServeRunsLocallyWithCooldown(t *testing.T) {
	rig := newDeviceRig(t, nil)
	var runs atomic.Int32
	rig.srv.RestartServe = func() error { runs.Add(1); return nil }
	now := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	rig.srv.now = func() time.Time { return now }
	_, plain := rig.device(t, false)
	_, full := rig.device(t, true)

	rec := rig.do(http.MethodPost, "/v1/rpc", plain, `{"id":"1","method":"gateway.restart_serve","params":{}}`)
	if rec.Code != http.StatusForbidden || runs.Load() != 0 {
		t.Fatalf("plain device: code %d runs %d", rec.Code, runs.Load())
	}

	type result struct {
		Result struct {
			Type     string `json:"type"`
			Accepted bool   `json:"accepted"`
			Reason   string `json:"reason"`
		} `json:"result"`
	}
	call := func(tok string) result {
		t.Helper()
		rec := rig.do(http.MethodPost, "/v1/rpc", tok, `{"id":"2","method":"gateway.restart_serve","params":{}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
		}
		var r result
		if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := call(full); r.Result.Type != "gateway_restart_serve" || !r.Result.Accepted {
		t.Fatalf("first = %+v", r)
	}
	now = now.Add(30 * time.Second)
	if r := call(hostToken); r.Result.Accepted || r.Result.Reason != "cooldown" {
		t.Fatalf("within 60s = %+v", r)
	}
	now = now.Add(31 * time.Second)
	if r := call(hostToken); !r.Result.Accepted {
		t.Fatalf("after 60s = %+v", r)
	}
	if runs.Load() != 2 || rig.forwards.Load() != 0 {
		t.Fatalf("runs %d forwards %d; restart_serve must not reach serve", runs.Load(), rig.forwards.Load())
	}
}
