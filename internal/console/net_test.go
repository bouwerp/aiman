package console

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestCaptureProxy(t *testing.T) {
	rpc := readyRPC()
	s, host := newTestServer(t, rpc, &fakeForwarder{}, nil)
	s.refresh(context.Background())
	if w := do(t, s, "GET", host, "/api/capture?run=r1", "", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("capture without token: %d", w.Code)
	}
	w := do(t, s, "GET", host, "/api/capture?run=r1&after=7&limit=9999", testToken, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("capture %d %s", w.Code, w.Body)
	}
	got := rpc.callsOf("harness.capture")
	if len(got) != 1 || got[0]["run"] != "r1" || got[0]["after_id"] != int64(7) || got[0]["limit"] != MaxCaptureLimit {
		t.Fatalf("capture params %v", got)
	}
	do(t, s, "GET", host, "/api/capture?run=r1", testToken, "", "")
	if got = rpc.callsOf("harness.capture"); got[1]["limit"] != DefaultCaptureLimit || got[1]["after_id"] != nil {
		t.Fatalf("default params %v", got[1])
	}
	for _, q := range []string{"?run=r1&after=-1", "?run=r1&after=x", "?run=r1&limit=0"} {
		if w = do(t, s, "GET", host, "/api/capture"+q, testToken, "", ""); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, w.Code)
		}
	}
	if w = do(t, s, "GET", host, "/api/capture?run=other", testToken, "", ""); w.Code != http.StatusConflict {
		t.Errorf("other run: %d", w.Code)
	}
}

func TestNetworkTabServed(t *testing.T) {
	s, host := newTestServer(t, readyRPC(), &fakeForwarder{}, nil)
	for _, p := range []string{"/net.js", "/net.css"} {
		if w := do(t, s, "GET", host, p, "", "", ""); w.Code != http.StatusOK || w.Body.Len() == 0 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	page := do(t, s, "GET", host, "/", "", "", "").Body.String()
	for _, want := range []string{`src="/net.js"`, `id="panel-net"`, `id="tab-net"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page misses %s", want)
		}
	}
	js := do(t, s, "GET", host, "/net.js", "", "", "").Body.String()
	if strings.Contains(js, "innerHTML") {
		t.Error("net.js writes HTML; every value must be set as text")
	}
}
