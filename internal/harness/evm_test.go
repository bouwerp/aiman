package harness

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/harness/edge"
	"gopkg.in/yaml.v3"
)

// errorData is Error(string) revert data for msg.
func errorData(msg string) string {
	pad := func(b []byte) []byte {
		out := make([]byte, (len(b)+31)/32*32)
		copy(out, b)
		return out
	}
	word := func(n int) []byte {
		w := make([]byte, 32)
		w[31] = byte(n)
		return w
	}
	b := append([]byte{0x08, 0xc3, 0x79, 0xa0}, word(32)...)
	b = append(b, word(len(msg))...)
	b = append(b, pad([]byte(msg))...)
	return "0x" + hex.EncodeToString(b)
}

func panicData(code byte) string {
	w := make([]byte, 32)
	w[31] = code
	return "0x4e487b71" + hex.EncodeToString(w)
}

const (
	addrA = "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"
	addrB = "0x5fbdb2315678afecb367f032d93f642f64180aa3"
	addrC = "0xe7f1725e7734ce288f8367e1bb143e90bb3f0512"
)

func traceFixture() string {
	nope := errorData("nope")
	return `{
  "type": "CALL", "from": "` + addrA + `", "to": "` + addrC + `", "value": "0xde0b6b3a7640000",
  "gas": "0x100000", "gasUsed": "0x6a2b", "input": "0xa9059cbb00000000",
  "output": "` + nope + `", "error": "execution reverted",
  "calls": [
    {"type": "STATICCALL", "from": "` + addrC + `", "to": "` + addrB + `", "gas": "0x1000", "gasUsed": "0x10",
     "input": "0x70a08231", "output": "0x01"},
    {"type": "DELEGATECALL", "from": "` + addrC + `", "to": "` + addrB + `", "gas": "0x2000", "gasUsed": "0x20",
     "input": "0x12345678", "output": "` + panicData(0x11) + `", "error": "execution reverted"},
    {"type": "CALL", "from": "` + addrC + `", "to": "` + addrB + `", "gas": "0xf0000", "gasUsed": "0x5000",
     "input": "0x", "output": "` + nope + `", "error": "execution reverted", "revertReason": "nope",
     "calls": [
       {"type": "CALL", "from": "` + addrB + `", "to": "` + addrA + `", "gas": "0x10", "gasUsed": "0x10",
        "input": "0xdeadbeef", "output": "0xcafebabe00", "error": "execution reverted"},
       {"type": "CALL", "from": "` + addrB + `", "to": "` + addrA + `", "gas": "0x10", "gasUsed": "0x10",
        "output": "0xbeef", "error": "execution reverted"},
       {"type": "CREATE", "from": "` + addrB + `", "to": "` + addrA + `", "gas": "0x10", "gasUsed": "0x10",
        "input": "0x6080604052", "error": "out of gas"}
     ]}
  ]
}`
}

func TestEVMTraceParse(t *testing.T) {
	root, frames, truncated, err := ParseCallTrace([]byte(traceFixture()))
	if err != nil {
		t.Fatal(err)
	}
	if frames != 7 || truncated {
		t.Fatalf("frames %d truncated %v", frames, truncated)
	}
	if root.Type != "CALL" || root.From != strings.ToLower(addrA) || root.To != addrC || root.Value != "1000000000000000000" ||
		root.Gas != 0x100000 || root.GasUsed != 0x6a2b || root.Selector != "0xa9059cbb" || root.InputBytes != 8 {
		t.Fatalf("root = %+v", root)
	}
	if root.Revert == nil || root.Revert.Kind != RevertError || root.Revert.Message != "nope" || root.Revert.Bytes != 100 {
		t.Fatalf("root revert = %+v", root.Revert)
	}
	if len(root.Calls) != 3 {
		t.Fatalf("calls = %d", len(root.Calls))
	}
	static := root.Calls[0]
	if static.Type != "STATICCALL" || static.Revert != nil || static.Error != "" || static.Value != "" {
		t.Fatalf("static call = %+v", static)
	}
	p := root.Calls[1].Revert
	if p == nil || p.Kind != RevertPanic || p.Code != "0x11" || p.Message != "arithmetic overflow or underflow" {
		t.Fatalf("panic = %+v", p)
	}
	inner := root.Calls[2]
	if len(inner.Calls) != 3 || inner.Revert.Kind != RevertError || inner.Revert.Message != "nope" {
		t.Fatalf("inner = %+v", inner)
	}
	if c := inner.Calls[0].Revert; c.Kind != RevertCustom || c.Selector != "0xcafebabe" || c.Data != "0xcafebabe00" {
		t.Fatalf("custom = %+v", c)
	}
	if r := inner.Calls[1].Revert; r.Kind != RevertRaw || r.Data != "0xbeef" || r.Bytes != 2 {
		t.Fatalf("raw = %+v", r)
	}
	create := inner.Calls[2]
	if create.Selector != "" || create.Error != "out of gas" || create.Revert.Kind != RevertEmpty {
		t.Fatalf("create = %+v", create)
	}
}

func TestEVMTraceParseRevertEdges(t *testing.T) {
	for _, tc := range []struct {
		data, kind, msg string
	}{
		{"", RevertEmpty, ""},
		{panicData(0x99), RevertPanic, "unknown panic code"},
		{panicData(0x32), RevertPanic, "array index out of bounds"},
		// An Error selector whose offset points past the data is custom, not a crash.
		{"0x08c379a0" + strings.Repeat("ff", 64), RevertCustom, ""},
		{"0x08c379a0" + strings.Repeat("00", 31) + "20" + strings.Repeat("00", 31) + "ff", RevertCustom, ""},
		{errorData("line\nbreak\x07"), RevertError, "line break"},
	} {
		b, _ := hexData(tc.data)
		r := DecodeRevert(b)
		if r.Kind != tc.kind || r.Message != tc.msg {
			t.Errorf("%s: %+v, want %s %q", tc.data, r, tc.kind, tc.msg)
		}
	}
	long := errorData(strings.Repeat("x", 5000))
	b, _ := hexData(long)
	if r := DecodeRevert(b); len(r.Message) > maxRevertMessage+len("…") || len(r.Data) != 2+2*maxEVMHexBytes {
		t.Fatalf("long revert message %d data %d", len(r.Message), len(r.Data))
	}

	// A tree past MaxEVMCalls keeps the first frames and says truncated.
	kids := make([]string, MaxEVMCalls+10)
	for i := range kids {
		kids[i] = `{"type":"CALL","from":"` + addrA + `","to":"` + addrB + `","gas":"0x1","gasUsed":"0x1"}`
	}
	raw := `{"type":"CALL","from":"` + addrA + `","to":"` + addrB + `","calls":[` + strings.Join(kids, ",") + `]}`
	root, frames, truncated, err := ParseCallTrace([]byte(raw))
	if err != nil || !truncated || frames != MaxEVMCalls || len(root.Calls) != MaxEVMCalls-1 {
		t.Fatalf("frames %d truncated %v calls %d err %v", frames, truncated, len(root.Calls), err)
	}
	for _, bad := range []string{`[]`, `{}`, `nope`} {
		if _, _, _, err := ParseCallTrace([]byte(bad)); err == nil {
			t.Errorf("%s parsed", bad)
		}
	}
}

const evmHarnessFile = `version: 1
app:
  run: sleep 1000
services:
  chain:
    image: ghcr.io/foundry-rs/foundry:v1.8.1
    evm: {kind: anvil, chain_id: 1337, args: [--block-time, "1"]}
scenarios:
  - id: plain
    services: [chain]
`

func TestEVMFileAndCompose(t *testing.T) {
	f, err := Parse([]byte(evmHarnessFile))
	if err != nil {
		t.Fatal(err)
	}
	sc, _ := f.Scenario("plain")
	if got := f.EVMServices(sc); len(got) != 1 || got[0] != "chain" {
		t.Fatalf("evm services %v", got)
	}
	p, err := Compose(f, RunSpec{
		RunID: "r1", Scenario: "plain", Src: "/h/src", Out: "/h/out", ControlDir: "/h/control", AimanBin: "/usr/bin/aiman",
		UID: 1000, GID: 1000, AppImage: "golang:1.25", EdgeImage: EdgeImage,
		InnerSubnet: netip.MustParsePrefix("10.231.0.0/25"), OuterSubnet: netip.MustParsePrefix("10.231.0.128/25"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Relays) != 1 || p.Relays[0].Kind != RelayKindEVM || p.Relays[0].Target != "chain:8545" {
		t.Fatalf("relays %+v", p.Relays)
	}
	var doc composeFile
	if err := yaml.Unmarshal(p.YAML, &doc); err != nil {
		t.Fatal(err)
	}
	chain := doc.Services["chain"]
	if strings.Join(chain.Entrypoint, " ") != "anvil" ||
		strings.Join(chain.Command, " ") != "--host 0.0.0.0 --port 8545 --chain-id 1337 --block-time 1" ||
		chain.Environment["HOME"] != "/tmp" || !chain.ReadOnly || chain.Healthcheck == nil ||
		!strings.Contains(chain.Healthcheck.Test[1], "cast block-number") {
		t.Fatalf("chain service %+v", chain)
	}
	if nets, _ := chain.Networks.([]any); len(nets) != 1 || nets[0] != NetworkInner {
		t.Fatalf("chain networks %v", chain.Networks)
	}
	if !strings.Contains(strings.Join(doc.Services[ServiceEdge].Command, " "), "--evm-relay chain=10001=chain:8545") {
		t.Fatalf("edge command %v", doc.Services[ServiceEdge].Command)
	}
	if !doc.Networks[NetworkInner].Internal {
		t.Fatal("inner network is not internal")
	}

	for _, bad := range []string{
		`evm: {kind: hardhat}`,
		`evm: {kind: anvil, args: [--fork-url, "https://rpc.example"]}`,
		`evm: {kind: anvil, args: ["--fork-url=https://rpc.example"]}`,
		`evm: {kind: anvil, args: [-f, x]}`,
		`evm: {kind: anvil, args: [--host, 1.2.3.4]}`,
		`evm: {kind: anvil, port: 70000}`,
		"evm: {kind: anvil}\n    database: {kind: postgres}",
	} {
		file := strings.Replace(evmHarnessFile, `evm: {kind: anvil, chain_id: 1337, args: [--block-time, "1"]}`, bad, 1)
		if _, err := Parse([]byte(file)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err %v, want invalid", bad, err)
		}
	}
}

// fakeEVMNode answers the read methods with a two-transaction chain and
// records every method it is asked for.
type fakeEVMNode struct {
	mu      sync.Mutex
	methods []string
	hits    int
}

func (n *fakeEVMNode) handler(t *testing.T) http.Handler {
	ok := "0x" + strings.Repeat("a", 64)
	bad := "0x" + strings.Repeat("b", 64)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var batch []struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.Unmarshal(body, &batch); err != nil {
			t.Errorf("not a batch: %s", body)
		}
		var out []map[string]any
		n.mu.Lock()
		n.hits++
		for _, c := range batch {
			n.methods = append(n.methods, c.Method)
			var res any
			switch c.Method {
			case "eth_blockNumber":
				res = "0x2"
			case "eth_chainId":
				res = "0x7a69"
			case "eth_getBlockByNumber":
				num := c.Params[0].(string)
				switch num {
				case "0x2":
					res = map[string]any{"number": num, "transactions": []any{map[string]any{
						"hash": bad, "blockNumber": num, "transactionIndex": "0x0", "from": addrA, "to": addrC, "value": "0x0", "input": "0xa9059cbb"}}}
				case "0x1":
					res = map[string]any{"number": num, "transactions": []any{map[string]any{
						"hash": ok, "blockNumber": num, "transactionIndex": "0x0", "from": addrA, "to": nil, "value": "0x0", "input": "0x6080"}}}
				default:
					res = map[string]any{"number": num, "transactions": []any{}}
				}
			case "eth_getTransactionReceipt":
				switch c.Params[0] {
				case ok:
					res = map[string]any{"status": "0x1", "gasUsed": "0x5208", "blockNumber": "0x1", "contractAddress": addrB}
				case bad:
					res = map[string]any{"status": "0x0", "gasUsed": "0x6a2b", "blockNumber": "0x2"}
				}
			case "debug_traceTransaction":
				res = json.RawMessage(traceFixture())
			}
			out = append(out, map[string]any{"jsonrpc": "2.0", "id": c.ID, "result": res})
		}
		n.mu.Unlock()
		_ = json.NewEncoder(w).Encode(out)
	})
}

// redirect sends every request to the fake node, whatever host port the
// run published.
type redirect struct{ to string }

func (rt redirect) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.URL.Scheme, r2.URL.Host = "http", rt.to
	return http.DefaultTransport.RoundTrip(r2)
}

// TestEVMTraceReadOnly checks that the console's EVM reads use only the
// relay's read methods, that a write is refused before anything is sent,
// and that the list and trace come back decoded.
func TestEVMTraceReadOnly(t *testing.T) {
	node := &fakeEVMNode{}
	srv := httptest.NewServer(node.handler(t))
	t.Cleanup(srv.Close)
	tm := newTestManager(t, func(o *Options) {
		o.Docker = newFakeDocker("golang:1.25", "ghcr.io/foundry-rs/foundry:v1.8.1", EdgeImage)
		o.AllowedImages = []string{"golang:*", "ghcr.io/foundry-rs/foundry:*"}
		o.EVMHTTP = &http.Client{Transport: redirect{to: strings.TrimPrefix(srv.URL, "http://")}}
	})
	run := tm.upReady(t, testRepo(t, evmHarnessFile), "plain")
	if len(run.EVM) != 1 || run.EVM[0] != "chain" {
		t.Fatalf("run evm %v", run.EVM)
	}

	// The client refuses a write before it reaches the node.
	c := evmClient{url: srv.URL, http: srv.Client()}
	for _, m := range []string{"eth_sendTransaction", "eth_sendRawTransaction", "eth_sign", "anvil_mine", "evm_revert", "eth_call"} {
		_, err := c.batch(context.Background(), []evmReq{{Method: "eth_blockNumber"}, {Method: m}})
		var me *edge.EVMMethodError
		if !errors.As(err, &me) {
			t.Errorf("%s: err %v", m, err)
		}
	}
	node.mu.Lock()
	hits := node.hits
	node.mu.Unlock()
	if hits != 0 {
		t.Fatalf("a refused batch reached the node %d times", hits)
	}

	list, err := tm.EVMTxs(context.Background(), run.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if list.Service != "chain" || list.ChainID != 31337 || list.Head != 2 || list.Blocks != 3 || len(list.Txs) != 2 {
		t.Fatalf("list %+v", list)
	}
	if tx := list.Txs[0]; tx.Status != EVMStatusReverted || tx.Selector != "0xa9059cbb" || tx.Block != 2 || tx.GasUsed != 0x6a2b {
		t.Fatalf("newest tx %+v", tx)
	}
	if tx := list.Txs[1]; tx.Status != EVMStatusSuccess || tx.Contract != addrB || tx.To != "" || tx.Selector != "" {
		t.Fatalf("creation tx %+v", tx)
	}

	tr, err := tm.EVMTrace(context.Background(), run.ID, "chain", list.Txs[0].Hash)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != EVMStatusReverted || tr.Revert == nil || tr.Revert.Message != "nope" || tr.Frames != 7 || tr.Root == nil {
		t.Fatalf("trace %+v", tr)
	}

	node.mu.Lock()
	methods := append([]string(nil), node.methods...)
	node.mu.Unlock()
	for _, m := range methods {
		if !edge.EVMMethodAllowed(m) {
			t.Fatalf("the node was asked for %s (all: %v)", m, methods)
		}
	}
	if _, err := tm.EVMTrace(context.Background(), run.ID, "", "0x12"); !errors.Is(err, ErrParams) {
		t.Fatalf("bad hash: %v", err)
	}
	if _, err := tm.EVMTxs(context.Background(), run.ID, "web", 0); !errors.Is(err, ErrParams) {
		t.Fatalf("not an evm service: %v", err)
	}
	if _, err := tm.EVMTxs(context.Background(), run.ID, "", MaxEVMBlocks+1); !errors.Is(err, ErrParams) {
		t.Fatalf("too many blocks: %v", err)
	}
	if err := tm.Down(context.Background(), run.ID, StopDown); err != nil {
		t.Fatal(err)
	}
	if _, err := tm.EVMTxs(context.Background(), run.ID, "", 0); err == nil {
		t.Fatal("a stopped run answered")
	}
}
