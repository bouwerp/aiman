package console

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/usecase"
)

func dbRPC() *fakeRPC {
	rpc := readyRPC()
	base := rpc.answer
	rpc.answer = func(method string, params map[string]any) (json.RawMessage, error) {
		if method == "harness.db" {
			if params["write"] == true {
				return nil, &usecase.RPCError{Code: "needs_operator", Message: "only the operator writes"}
			}
			return json.RawMessage(`{"type":"harness_db","run":"r1","service":"db","kind":"postgres","op":"rows","role":"aiman_ro","limit":200,"columns":["id"],"rows":[["42"]],"truncated":false,"redacted":0}`), nil
		}
		return base(method, params)
	}
	return rpc
}

func TestDBTabChecks(t *testing.T) {
	rpc := dbRPC()
	s, host := newTestServer(t, rpc, &fakeForwarder{}, nil)
	s.refresh(context.Background())
	origin := "http://" + host

	// The DB paths take the same token, Host and Origin checks as the rest.
	if w := do(t, s, "GET", host, "/api/db?run=r1&service=db&op=tables", "", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", w.Code)
	}
	if w := do(t, s, "GET", "evil.example:"+strings.Split(host, ":")[1], "/api/db?run=r1&service=db&op=tables", testToken, "", ""); w.Code != http.StatusForbidden {
		t.Fatalf("bad Host: %d", w.Code)
	}
	body := `{"run":"r1","service":"db","sql":"select 1"}`
	for _, o := range []string{"", "http://evil.example"} {
		if w := do(t, s, "POST", host, "/api/db/query", testToken, o, body); w.Code != http.StatusForbidden {
			t.Fatalf("Origin %q: %d", o, w.Code)
		}
	}
	if n := len(rpc.callsOf("harness.db")); n != 0 {
		t.Fatalf("refused requests reached serve %d times", n)
	}
	for _, p := range []string{"/db.js", "/db.css"} {
		if w := do(t, s, "GET", host, p, "", "", ""); w.Code != http.StatusOK {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}

	// Reads go to harness.db for the shown run.
	w := do(t, s, "GET", host, "/api/db?run=r1&service=db&op=rows&table=public.orders&limit=20", testToken, "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"role":"aiman_ro"`) {
		t.Fatalf("rows %d %s", w.Code, w.Body)
	}
	got := rpc.callsOf("harness.db")
	if len(got) != 1 || got[0]["run"] != "r1" || got[0]["op"] != "rows" || got[0]["table"] != "public.orders" || got[0]["limit"] != 20 {
		t.Fatalf("params %v", got)
	}
	if w = do(t, s, "POST", host, "/api/db/query", testToken, origin, body); w.Code != http.StatusOK {
		t.Fatalf("query %d %s", w.Code, w.Body)
	}
	if got = rpc.callsOf("harness.db"); got[1]["op"] != "query" || got[1]["sql"] != "select 1" || got[1]["write"] != nil {
		t.Fatalf("query params %v", got[1])
	}
	// A write is passed on as a write, and serve's refusal comes back as is.
	w = do(t, s, "POST", host, "/api/db/query", testToken, origin, `{"run":"r1","service":"db","sql":"delete from t","write":true}`)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "needs_operator") {
		t.Fatalf("write %d %s", w.Code, w.Body)
	}

	for _, q := range []string{"?run=r1&op=tables", "?run=r1&service=db&op=query", "?run=r1&service=db&op=rows", "?run=r1&service=db&op=tables&limit=0"} {
		if w = do(t, s, "GET", host, "/api/db"+q, testToken, "", ""); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, w.Code)
		}
	}
	if w = do(t, s, "GET", host, "/api/db?run=other&service=db&op=tables", testToken, "", ""); w.Code != http.StatusConflict {
		t.Errorf("other run: %d", w.Code)
	}
	if w = do(t, s, "POST", host, "/api/db/query", testToken, origin, `{"run":"other","service":"db","sql":"select 1"}`); w.Code != http.StatusConflict {
		t.Errorf("other run query: %d", w.Code)
	}
}
