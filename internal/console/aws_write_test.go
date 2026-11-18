package console

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAWSWriteEndpoint(t *testing.T) {
	rpc := readyRPC()
	base := rpc.answer
	rpc.answer = func(method string, params map[string]any) (json.RawMessage, error) {
		if method == "harness.aws_write" {
			return json.RawMessage(`{"type":"harness_aws_write","op":"` + params["op"].(string) + `"}`), nil
		}
		return base(method, params)
	}
	s, host := newTestServer(t, rpc, &fakeForwarder{}, nil)
	s.refresh(context.Background())
	origin := "http://" + host

	body := `{"run":"r1","op":"reset"}`
	if w := do(t, s, "POST", host, AWSWritePath, "", origin, body); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", w.Code)
	}
	for _, o := range []string{"", "http://evil.example"} {
		if w := do(t, s, "POST", host, AWSWritePath, testToken, o, body); w.Code != http.StatusForbidden {
			t.Fatalf("Origin %q: %d", o, w.Code)
		}
	}
	if w := do(t, s, "GET", host, AWSWritePath, testToken, "", ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", w.Code)
	}
	if n := len(rpc.callsOf("harness.aws_write")); n != 0 {
		t.Fatalf("refused requests reached serve %d times", n)
	}

	if w := do(t, s, "POST", host, AWSWritePath, testToken, origin, body); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"op":"reset"`) {
		t.Fatalf("reset %d %s", w.Code, w.Body)
	}
	data := base64.StdEncoding.EncodeToString([]byte("hello"))
	put := `{"run":"r1","service":"aws","op":"s3_put","bucket":"uploads","key":"in/a.txt","data":"` + data + `","content_type":"text/plain"}`
	if w := do(t, s, "POST", host, AWSWritePath, testToken, origin, put); w.Code != http.StatusOK {
		t.Fatalf("put %d %s", w.Code, w.Body)
	}
	item := `{"run":"r1","op":"ddb_put","table":"orders","item":{"pk":"o-1","n":12345678901234567890}}`
	if w := do(t, s, "POST", host, AWSWritePath, testToken, origin, item); w.Code != http.StatusOK {
		t.Fatalf("item %d %s", w.Code, w.Body)
	}
	got := rpc.callsOf("harness.aws_write")
	if len(got) != 3 || got[0]["op"] != "reset" || got[0]["run"] != "r1" || got[1]["data"] != data || got[1]["service"] != "aws" || got[1]["content_type"] != "text/plain" {
		t.Fatalf("params %v", got)
	}
	if b, _ := json.Marshal(got[2]["item"]); string(b) != `{"pk":"o-1","n":12345678901234567890}` {
		t.Fatalf("item %s", b)
	}

	big := base64.StdEncoding.EncodeToString(make([]byte, ConsoleAWSPutMax+1))
	for name, b := range map[string]string{
		"unknown op":  `{"run":"r1","op":"s3_copy"}`,
		"bad data":    `{"run":"r1","op":"s3_put","data":"%%"}`,
		"too big":     `{"run":"r1","op":"s3_put","bucket":"b","key":"k","data":"` + big + `"}`,
		"extra field": `{"run":"r1","op":"reset","sql":"x"}`,
	} {
		if w := do(t, s, "POST", host, AWSWritePath, testToken, origin, b); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if w := do(t, s, "POST", host, AWSWritePath, testToken, origin, `{"run":"other","op":"reset"}`); w.Code != http.StatusConflict {
		t.Errorf("other run: %d", w.Code)
	}
	if n := len(rpc.callsOf("harness.aws_write")); n != 3 {
		t.Fatalf("refused requests reached serve: %d calls", n)
	}
}
