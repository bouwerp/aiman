package console

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestEVMTraceReadOnlyConsole checks the EVM tab's API: it only asks serve
// for harness.evm_txs and harness.evm_trace, on the run it shows, and it
// refuses a POST, a bad hash or a missing token before calling serve.
func TestEVMTraceReadOnlyConsole(t *testing.T) {
	tx := "0x" + strings.Repeat("ab", 32)
	rpc := &fakeRPC{answer: func(method string, params map[string]any) (json.RawMessage, error) {
		switch method {
		case "harness.status":
			return runJSON("ready"), nil
		case "harness.evm_txs":
			return json.RawMessage(`{"type":"harness_evm_txs","evm":{"run":"r1","service":"chain","chain_id":31337,"head":2,"blocks":3,"txs":[],"truncated":false}}`), nil
		case "harness.evm_trace":
			return json.RawMessage(`{"type":"harness_evm_trace","trace":{"run":"r1","service":"chain","tx":"` + tx + `","block":2,"status":"reverted","gas_used":1,"root":{"type":"CALL","from":"0x00","gas":1,"gas_used":1,"input_bytes":0,"output_bytes":0},"frames":1,"truncated":false}}`), nil
		}
		return nil, fmt.Errorf("unexpected %s", method)
	}}
	s, host := newTestServer(t, rpc, directForwarder{}, nil)
	s.refresh(context.Background())

	if w := do(t, s, http.MethodGet, host, EVMTxsPath+"?run=r1&service=chain&blocks=5", testToken, "", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "harness_evm_txs") {
		t.Fatalf("txs %d %s", w.Code, w.Body)
	}
	if got := rpc.callsOf("harness.evm_txs"); len(got) != 1 || got[0]["run"] != "r1" || got[0]["service"] != "chain" || got[0]["blocks"] != 5 {
		t.Fatalf("evm_txs calls %v", got)
	}
	if w := do(t, s, http.MethodGet, host, EVMTracePath+"?run=r1&tx="+tx, testToken, "", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "reverted") {
		t.Fatalf("trace %d %s", w.Code, w.Body)
	}
	if got := rpc.callsOf("harness.evm_trace"); len(got) != 1 || got[0]["tx"] != tx || got[0]["service"] != nil {
		t.Fatalf("evm_trace calls %v", got)
	}

	for _, c := range []struct {
		method, path, token string
		code                int
	}{
		{http.MethodGet, EVMTracePath + "?run=r1&tx=0x12", testToken, http.StatusBadRequest},
		{http.MethodGet, EVMTracePath + "?run=r1&tx=" + tx + "&service=Bad.Name", testToken, http.StatusBadRequest},
		{http.MethodGet, EVMTxsPath + "?run=r1&blocks=500", testToken, http.StatusBadRequest},
		{http.MethodGet, EVMTxsPath + "?run=r9", testToken, http.StatusConflict},
		{http.MethodGet, EVMTxsPath + "?run=r1", "", http.StatusUnauthorized},
		{http.MethodPost, EVMTxsPath + "?run=r1", testToken, http.StatusUnsupportedMediaType},
	} {
		if w := do(t, s, c.method, host, c.path, c.token, "http://"+host, ""); w.Code != c.code {
			t.Errorf("%s %s: %d, want %d (%s)", c.method, c.path, w.Code, c.code, w.Body)
		}
	}
	if n := len(rpc.callsOf("harness.evm_txs")) + len(rpc.callsOf("harness.evm_trace")); n != 2 {
		t.Fatalf("refused requests reached serve: %d calls", n)
	}
	for _, path := range []string{"/evm.js", "/evm.css"} {
		if w := do(t, s, http.MethodGet, host, path, "", "", ""); w.Code != http.StatusOK {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
}
