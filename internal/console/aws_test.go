package console

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/usecase"
)

func awsRPC() *fakeRPC {
	return &fakeRPC{answer: func(method string, params map[string]any) (json.RawMessage, error) {
		switch method {
		case "harness.status":
			return runJSON("ready"), nil
		case "harness.aws":
			return json.RawMessage(fmt.Sprintf(`{"type":"harness_aws","run":"r1","service":"aws","op":%q,"truncated":false,"redacted":0}`, params["op"])), nil
		case "harness.aws_calls":
			return json.RawMessage(`{"type":"harness_aws_calls","run":"r1","service":"aws","calls":[],"next":0,"more":false,"total":0,"dropped":0,"redacted":0}`), nil
		}
		return nil, fmt.Errorf("unexpected %s", method)
	}}
}

// TestAWSConsoleReads checks the AWS tab's read API: each GET becomes one
// harness.aws or harness.aws_calls call on the shown run, with the params
// typed as serve takes them.
func TestAWSConsoleReads(t *testing.T) {
	rpc := awsRPC()
	s, host := newTestServer(t, rpc, directForwarder{}, nil)
	s.refresh(context.Background())

	get := func(path string) {
		t.Helper()
		if w := do(t, s, http.MethodGet, host, path, testToken, "", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"type":"harness_aws`) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	get(AWSPath + "?run=r1&op=s3_objects&service=aws&bucket=orders&prefix=seed%2F&delimiter=%2F&token=abc&limit=10")
	get(AWSPath + "?run=r1&op=s3_download&bucket=orders&key=a%20b.txt&offset=1048576&version=v1")
	get(AWSPath + "?run=r1&op=ddb_query&table=orders&index=by-status&pk_name=status&pk_value=new&sk_op=between&sk_value=a&sk_value=m&forward=false&start=tok")
	get(AWSPath + "?run=r1&op=ddb_get&table=orders&key=" + url.QueryEscape(`{"pk":"o1","n":7}`))
	get(AWSPath + "?run=r1&op=s3_objects&bucket=b&delimiter=")
	get(AWSCallsPath + "?run=r1&after=12&limit=999")
	get(AWSCallsPath + "?run=r1")

	calls := rpc.callsOf("harness.aws")
	if len(calls) != 5 {
		t.Fatalf("harness.aws calls %v", calls)
	}
	if c := calls[0]; c["run"] != "r1" || c["service"] != "aws" || c["op"] != "s3_objects" || c["bucket"] != "orders" ||
		c["prefix"] != "seed/" || c["delimiter"] != "/" || c["token"] != "abc" || c["limit"] != int64(10) {
		t.Errorf("s3_objects params %v", c)
	}
	if c := calls[1]; c["key"] != "a b.txt" || c["offset"] != int64(1048576) || c["version"] != "v1" || c["service"] != nil {
		t.Errorf("s3_download params %v", c)
	}
	q := calls[2]
	pk, _ := q["pk"].(map[string]any)
	sk, _ := q["sk"].(map[string]any)
	if pk["name"] != "status" || pk["value"] != "new" || sk["op"] != "between" || fmt.Sprint(sk["values"]) != "[a m]" ||
		q["forward"] != false || q["start"] != "tok" || q["index"] != "by-status" {
		t.Errorf("ddb_query params %v", q)
	}
	key, _ := calls[3]["key"].(map[string]any)
	if key["pk"] != "o1" || fmt.Sprint(key["n"]) != "7" {
		t.Errorf("ddb_get key %v", calls[3]["key"])
	}
	if d, ok := calls[4]["delimiter"]; !ok || d != "" {
		t.Errorf("empty delimiter not passed: %v", calls[4])
	}
	ac := rpc.callsOf("harness.aws_calls")
	if len(ac) != 2 || ac[0]["after_id"] != int64(12) || ac[0]["limit"] != MaxAWSCallsLimit || ac[0]["run"] != "r1" ||
		ac[1]["limit"] != DefaultAWSCallsLimit || ac[1]["after_id"] != nil {
		t.Fatalf("aws_calls params %v", ac)
	}
}

// TestAWSConsoleRefusals checks that a bad request, another run, a missing
// token or a POST to a read route never reaches serve.
func TestAWSConsoleRefusals(t *testing.T) {
	rpc := awsRPC()
	s, host := newTestServer(t, rpc, directForwarder{}, nil)
	s.refresh(context.Background())
	origin := "http://" + host
	for _, c := range []struct {
		method, path, token, ctype string
		code                       int
	}{
		{http.MethodGet, AWSPath + "?run=r1&op=s3_put", testToken, "", http.StatusBadRequest},
		{http.MethodGet, AWSPath + "?run=r1", testToken, "", http.StatusBadRequest},
		{http.MethodGet, AWSPath + "?run=r1&op=s3_objects&service=Bad.Name", testToken, "", http.StatusBadRequest},
		{http.MethodGet, AWSPath + "?run=r1&op=s3_object&offset=-1", testToken, "", http.StatusBadRequest},
		{http.MethodGet, AWSPath + "?run=r1&op=ddb_scan&limit=0", testToken, "", http.StatusBadRequest},
		{http.MethodGet, AWSPath + "?run=r1&op=ddb_query&forward=maybe", testToken, "", http.StatusBadRequest},
		{http.MethodGet, AWSPath + "?run=r1&op=ddb_get&key=o1", testToken, "", http.StatusBadRequest},
		{http.MethodGet, AWSPath + "?run=r1&op=ddb_get&key=%7B%7D", testToken, "", http.StatusBadRequest},
		{http.MethodGet, AWSPath + "?run=r1&op=ddb_query&sk_op=between&sk_value=a", testToken, "", http.StatusBadRequest},
		{http.MethodGet, AWSPath + "?run=r1&op=ddb_query&sk_op=like&sk_value=a", testToken, "", http.StatusBadRequest},
		{http.MethodGet, AWSPath + "?run=r1&op=ddb_query&sk_value=a", testToken, "", http.StatusBadRequest},
		{http.MethodGet, AWSCallsPath + "?run=r1&after=-1", testToken, "", http.StatusBadRequest},
		{http.MethodGet, AWSCallsPath + "?run=r1&limit=x", testToken, "", http.StatusBadRequest},
		{http.MethodGet, AWSPath + "?run=r9&op=resources", testToken, "", http.StatusConflict},
		{http.MethodGet, AWSCallsPath + "?run=r9", testToken, "", http.StatusConflict},
		{http.MethodGet, AWSPath + "?run=r1&op=resources", "", "", http.StatusUnauthorized},
		{http.MethodGet, AWSCallsPath + "?run=r1", "nope", "", http.StatusUnauthorized},
		{http.MethodPost, AWSPath + "?run=r1&op=resources", testToken, "application/json", http.StatusMethodNotAllowed},
		{http.MethodPost, AWSCallsPath + "?run=r1", testToken, "application/json", http.StatusMethodNotAllowed},
	} {
		body := ""
		if c.ctype != "" {
			body = "{}"
		}
		w := do(t, s, c.method, host, c.path, c.token, origin, body)
		if w.Code != c.code {
			t.Errorf("%s %s: %d, want %d (%s)", c.method, c.path, w.Code, c.code, w.Body)
		}
	}
	if n := len(rpc.callsOf("harness.aws")) + len(rpc.callsOf("harness.aws_calls")); n != 0 {
		t.Fatalf("refused requests reached serve: %d calls", n)
	}
	// Host is checked before anything else.
	if w := do(t, s, http.MethodGet, "evil.example:80", AWSPath+"?run=r1&op=resources", testToken, "", ""); w.Code != http.StatusForbidden {
		t.Errorf("foreign Host: %d", w.Code)
	}
}

// TestAWSConsoleServeRefusal relays serve's refusal as 502 with its code.
func TestAWSConsoleServeRefusal(t *testing.T) {
	rpc := &fakeRPC{answer: func(method string, _ map[string]any) (json.RawMessage, error) {
		if method == "harness.status" {
			return runJSON("ready"), nil
		}
		return nil, &usecase.RPCError{Code: "invalid_params", Message: "run r1 starts no aws service"}
	}}
	s, host := newTestServer(t, rpc, directForwarder{}, nil)
	s.refresh(context.Background())
	w := do(t, s, http.MethodGet, host, AWSPath+"?run=r1&op=s3_buckets", testToken, "", "")
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "invalid_params") || !strings.Contains(w.Body.String(), "starts no aws service") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestAWSTabServed(t *testing.T) {
	s, host := newTestServer(t, awsRPC(), directForwarder{}, nil)
	for _, p := range []string{"/aws.js", "/aws.css"} {
		if w := do(t, s, http.MethodGet, host, p, "", "", ""); w.Code != http.StatusOK || w.Body.Len() == 0 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	page := do(t, s, http.MethodGet, host, "/", "", "", "").Body.String()
	for _, want := range []string{`src="/aws.js"`, `href="/aws.css"`, `id="panel-aws"`, `id="tab-aws"`, `id="aws-write"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page misses %s", want)
		}
	}
}
