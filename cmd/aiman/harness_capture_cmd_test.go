package main

import (
	"strings"
	"testing"
)

func TestHarnessCmdCapture(t *testing.T) {
	f := &fakeHarness{t: t, answers: map[string][]any{
		"harness.capture": {`{"type":"harness_capture","run":"r1","network":"offline","state":"ready","total":2,"dropped":0,"next":2,"more":true,` +
			`"flows":[{"id":2,"start":"2026-11-17T12:00:00Z","duration_ms":3,"method":"POST","url":"https://api.stripe.com/v1/refunds","host":"api.stripe.com","tls":true,"status":200,"outcome":"stub","request_body_bytes":0,"response_body_bytes":2},` +
			`{"id":3,"start":"2026-11-17T12:00:01Z","duration_ms":0,"method":"CONNECT","url":"https://pinned.example","host":"pinned.example","tls":true,"outcome":"error","error":"tls handshake: bad certificate","request_body_bytes":0,"response_body_bytes":0}]}`},
	}}
	out, _, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "capture", "--after", "1", "--limit", "2")
	if err != nil {
		t.Fatal(err)
	}
	p := f.calls[0].params
	if p["session"] != "s1" || p["after_id"] != int64(1) || p["limit"] != 2 {
		t.Fatalf("params %v", p)
	}
	for _, want := range []string{"run r1, offline, 2 calls", "POST", "200 stub", "https://api.stripe.com/v1/refunds", "tls handshake: bad certificate", "--after 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
	if _, _, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "capture", "--after", "-1"); err == nil {
		t.Fatal("negative --after accepted")
	}
}
