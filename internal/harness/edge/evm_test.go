package edge

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeNode is an EVM node that records every method it is asked for.
type fakeNode struct {
	mu      sync.Mutex
	methods []string
}

func (n *fakeNode) seen() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.methods...)
}

func (n *fakeNode) start(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var batch []map[string]any
		if json.Unmarshal(body, &batch) != nil {
			var one map[string]any
			_ = json.Unmarshal(body, &one)
			batch = []map[string]any{one}
		}
		out := []map[string]any{}
		for _, c := range batch {
			m, _ := c["method"].(string)
			n.mu.Lock()
			n.methods = append(n.methods, m)
			n.mu.Unlock()
			out = append(out, map[string]any{"jsonrpc": "2.0", "id": c["id"], "result": "0x2a"})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return ln.Addr().String()
}

func TestFilterEVMRequest(t *testing.T) {
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"eth_sendTransaction","params":[{}]}`,
		`{"jsonrpc":"2.0","id":1,"method":"eth_sendRawTransaction","params":["0x00"]}`,
		`{"jsonrpc":"2.0","id":1,"method":"eth_sign","params":[]}`,
		`{"jsonrpc":"2.0","id":1,"method":"anvil_setBalance","params":[]}`,
		`{"jsonrpc":"2.0","id":1,"method":"evm_mine"}`,
		`{"jsonrpc":"2.0","id":1,"method":"eth_call","params":[{}]}`,
		`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","method":"eth_sendTransaction"}`,
		`[{"id":1,"method":"eth_blockNumber"},{"id":2,"method":"anvil_impersonateAccount","params":["0x00"]}]`,
	} {
		_, err := FilterEVMRequest([]byte(body))
		var me *EVMMethodError
		if !errors.As(err, &me) {
			t.Errorf("%s: err %v, want a method refusal", body, err)
		}
	}
	for _, body := range []string{``, `[]`, `"x"`, `{"method":1}`, `{"method":"eth_blockNumber","params":"0x1"}`} {
		if _, err := FilterEVMRequest([]byte(body)); err == nil {
			t.Errorf("%q passed the filter", body)
		}
	}
	got, err := FilterEVMRequest([]byte(`{"jsonrpc":"1.0","id":7,"method":"eth_getTransactionReceipt","params":["0xab"],"extra":{"method":"eth_sendTransaction"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"jsonrpc":"2.0","id":7,"method":"eth_getTransactionReceipt","params":["0xab"]}` {
		t.Fatalf("rebuilt request %s", got)
	}
}

// TestEVMRelayReadOnly drives a real KindEVM relay: reads reach the node,
// and a write, a batch carrying one, a GET or a WebSocket upgrade never do.
func TestEVMRelayReadOnly(t *testing.T) {
	node := &fakeNode{}
	target := node.start(t)
	e := newTestEdge(t)
	st, err := e.Add(RelaySpec{Name: "chain", Target: target, Kind: KindEVM})
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + st.Listen + "/"
	post := func(body string) (int, string) {
		resp, err := http.Post(url, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := post(`[{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber"},{"jsonrpc":"2.0","id":2,"method":"debug_traceTransaction","params":["0x01",{"tracer":"callTracer"}]}]`); code != http.StatusOK || !strings.Contains(body, "0x2a") {
		t.Fatalf("read: %d %s", code, body)
	}
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"eth_sendTransaction","params":[{"from":"0x00"}]}`,
		`[{"jsonrpc":"2.0","id":1,"method":"eth_chainId"},{"jsonrpc":"2.0","id":2,"method":"anvil_setCode","params":["0x00","0x00"]}]`,
	} {
		if code, out := post(body); code != http.StatusForbidden || !strings.Contains(out, "not allowed") {
			t.Fatalf("write %s: %d %s", body, code, out)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET upgrade: %d", resp.StatusCode)
	}
	big := `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":["` + strings.Repeat("a", EVMMaxRequestBytes) + `"]}`
	if code, _ := post(big); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body: %d", code)
	}
	for _, m := range node.seen() {
		if !EVMMethodAllowed(m) {
			t.Fatalf("the node saw %s; it saw %v", m, node.seen())
		}
	}
	if got := node.seen(); len(got) != 2 {
		t.Fatalf("node saw %v, want the two reads", got)
	}
	if st := e.List(); len(st) != 1 || st[0].Refused < 4 {
		t.Fatalf("status %+v", st)
	}
}
