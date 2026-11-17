package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/bouwerp/aiman/internal/harness/edge"
)

// EVM read bounds.
const (
	// DefaultEVMBlocks and MaxEVMBlocks are how many recent blocks
	// harness.evm_txs scans.
	DefaultEVMBlocks = 20
	MaxEVMBlocks     = edge.EVMMaxBatch
	// MaxEVMTxs caps the transactions one list holds, newest first.
	MaxEVMTxs = 100
	// EVMTimeout bounds one evm call to the node through the edge.
	EVMTimeout = 20 * time.Second
)

var txHashRe = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)

// EVMTx is one transaction of a recent block. Status is success, reverted
// or unknown (no receipt); Contract is the address a creation made.
type EVMTx struct {
	Hash     string `json:"hash"`
	Block    uint64 `json:"block"`
	Index    uint64 `json:"index"`
	From     string `json:"from"`
	To       string `json:"to,omitempty"`
	Contract string `json:"contract,omitempty"`
	Value    string `json:"value,omitempty"`
	Selector string `json:"selector,omitempty"`
	Status   string `json:"status"`
	GasUsed  uint64 `json:"gas_used"`
}

// EVMTxList is harness.evm_txs: the newest transactions of the last Blocks
// blocks up to Head.
type EVMTxList struct {
	Run       string  `json:"run"`
	Service   string  `json:"service"`
	ChainID   uint64  `json:"chain_id"`
	Head      uint64  `json:"head"`
	Blocks    int     `json:"blocks"`
	Txs       []EVMTx `json:"txs"`
	Truncated bool    `json:"truncated"`
}

// EVMTrace is harness.evm_trace: one transaction's receipt facts and its
// call tree. Revert is the top frame's decoded reason when it failed.
type EVMTrace struct {
	Run       string     `json:"run"`
	Service   string     `json:"service"`
	Tx        string     `json:"tx"`
	Block     uint64     `json:"block"`
	Status    string     `json:"status"`
	GasUsed   uint64     `json:"gas_used"`
	Contract  string     `json:"contract,omitempty"`
	Root      *EVMCall   `json:"root"`
	Revert    *EVMRevert `json:"revert,omitempty"`
	Frames    int        `json:"frames"`
	Truncated bool       `json:"truncated"`
}

// Transaction statuses.
const (
	EVMStatusSuccess  = "success"
	EVMStatusReverted = "reverted"
	EVMStatusUnknown  = "unknown"
)

// evmClient calls an EVM node's JSON-RPC through the edge's read-only relay.
// It refuses every method outside edge.EVMReadMethods before anything is
// sent, as the relay does again on its side.
type evmClient struct {
	url  string
	http *http.Client
}

type evmReq struct {
	Method string
	Params []any
}

type evmResp struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// batch sends calls as one JSON-RPC batch and returns each result in call
// order. A call the node refused is an error naming it.
func (c evmClient) batch(ctx context.Context, calls []evmReq) ([]json.RawMessage, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	if len(calls) > edge.EVMMaxBatch {
		return nil, fmt.Errorf("evm: %d calls in one batch; at most %d", len(calls), edge.EVMMaxBatch)
	}
	body := make([]map[string]any, 0, len(calls))
	for i, call := range calls {
		if !edge.EVMMethodAllowed(call.Method) {
			return nil, &edge.EVMMethodError{Method: call.Method}
		}
		params := call.Params
		if params == nil {
			params = []any{}
		}
		body = append(body, map[string]any{"jsonrpc": "2.0", "id": i, "method": call.Method, "params": params})
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("evm node: %w", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, edge.EVMMaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("evm node: %w", err)
	}
	if len(out) > edge.EVMMaxResponseBytes {
		return nil, errors.New("evm node: the answer is too large")
	}
	var answers []evmResp
	if err := json.Unmarshal(out, &answers); err != nil {
		var one evmResp
		if json.Unmarshal(out, &one) == nil && one.Error != nil {
			return nil, fmt.Errorf("evm node: %s", oneLine(one.Error.Message, 300))
		}
		return nil, fmt.Errorf("evm node: HTTP %d: not a JSON-RPC batch answer", resp.StatusCode)
	}
	results := make([]json.RawMessage, len(calls))
	seen := make([]bool, len(calls))
	for _, a := range answers {
		if a.ID < 0 || a.ID >= len(calls) || seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		if a.Error != nil {
			return nil, fmt.Errorf("evm node: %s: %s", calls[a.ID].Method, oneLine(a.Error.Message, 300))
		}
		results[a.ID] = a.Result
	}
	for i := range calls {
		if !seen[i] {
			return nil, fmt.Errorf("evm node: no answer to %s", calls[i].Method)
		}
	}
	return results, nil
}

// evmTarget finds a ready run's EVM service and the client for its relay.
// An empty service names the run's only EVM service.
func (m *Manager) evmTarget(id, service string) (string, evmClient, error) {
	lr, err := m.readyRunOf(id)
	if err != nil {
		return "", evmClient{}, err
	}
	run := lr.snapshot()
	if len(run.EVM) == 0 {
		return "", evmClient{}, paramsError(fmt.Sprintf("run %s starts no evm service", id))
	}
	if service == "" {
		if len(run.EVM) > 1 {
			return "", evmClient{}, paramsError(fmt.Sprintf("run %s has several evm services; name one of %v", id, run.EVM))
		}
		service = run.EVM[0]
	}
	if !slices.Contains(run.EVM, service) {
		return "", evmClient{}, paramsError(fmt.Sprintf("%q is not an evm service of run %s; it has %v", service, id, run.EVM))
	}
	port := 0
	for _, p := range run.Ports {
		if p.Name == service && p.Service == service {
			port = p.HostPort
		}
	}
	if port == 0 {
		return "", evmClient{}, &Error{Code: CodeNotRunning, Msg: fmt.Sprintf("run %s publishes no relay for %s", id, service)}
	}
	hc := m.opts.EVMHTTP
	if hc == nil {
		hc = &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	}
	return service, evmClient{url: "http://127.0.0.1:" + strconv.Itoa(port) + "/", http: hc}, nil
}

type rawBlock struct {
	Number       string  `json:"number"`
	Transactions []rawTx `json:"transactions"`
}

type rawTx struct {
	Hash             string `json:"hash"`
	BlockNumber      string `json:"blockNumber"`
	TransactionIndex string `json:"transactionIndex"`
	From             string `json:"from"`
	To               string `json:"to"`
	Value            string `json:"value"`
	Input            string `json:"input"`
}

type rawReceipt struct {
	Status          string `json:"status"`
	GasUsed         string `json:"gasUsed"`
	BlockNumber     string `json:"blockNumber"`
	ContractAddress string `json:"contractAddress"`
}

func (r *rawReceipt) status() string {
	switch {
	case r == nil:
		return EVMStatusUnknown
	case hexUint(r.Status) == 1:
		return EVMStatusSuccess
	case r.Status != "":
		return EVMStatusReverted
	}
	return EVMStatusUnknown
}

// EVMTxs lists the newest transactions of the last blocks blocks (default
// 20, at most 64) of a ready run's EVM service, with their receipts.
func (m *Manager) EVMTxs(ctx context.Context, id, service string, blocks int) (EVMTxList, error) {
	if blocks < 0 || blocks > MaxEVMBlocks {
		return EVMTxList{}, paramsError(fmt.Sprintf("blocks must be 1-%d", MaxEVMBlocks))
	}
	if blocks == 0 {
		blocks = DefaultEVMBlocks
	}
	service, c, err := m.evmTarget(id, service)
	if err != nil {
		return EVMTxList{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, EVMTimeout)
	defer cancel()
	head, err := c.batch(ctx, []evmReq{{Method: "eth_blockNumber"}, {Method: "eth_chainId"}})
	if err != nil {
		return EVMTxList{}, err
	}
	var headHex, chainHex string
	_ = json.Unmarshal(head[0], &headHex)
	_ = json.Unmarshal(head[1], &chainHex)
	list := EVMTxList{Run: id, Service: service, ChainID: hexUint(chainHex), Head: hexUint(headHex), Txs: []EVMTx{}}
	n := uint64(blocks)
	if n > list.Head+1 {
		n = list.Head + 1
	}
	list.Blocks = int(n)
	calls := make([]evmReq, 0, n)
	for i := range n {
		calls = append(calls, evmReq{Method: "eth_getBlockByNumber", Params: []any{"0x" + strconv.FormatUint(list.Head-i, 16), true}})
	}
	raws, err := c.batch(ctx, calls)
	if err != nil {
		return EVMTxList{}, err
	}
	for _, raw := range raws {
		var b *rawBlock
		if err := json.Unmarshal(raw, &b); err != nil || b == nil {
			continue
		}
		for i := len(b.Transactions) - 1; i >= 0; i-- {
			if len(list.Txs) >= MaxEVMTxs {
				list.Truncated = true
				break
			}
			list.Txs = append(list.Txs, txOf(b.Transactions[i]))
		}
	}
	if err := m.evmReceipts(ctx, c, list.Txs); err != nil {
		return EVMTxList{}, err
	}
	return list, nil
}

func txOf(t rawTx) EVMTx {
	tx := EVMTx{
		Hash: t.Hash, Block: hexUint(t.BlockNumber), Index: hexUint(t.TransactionIndex),
		From: lowerHex(t.From), To: lowerHex(t.To), Value: hexDecimal(t.Value), Status: EVMStatusUnknown,
	}
	if !txHashRe.MatchString(tx.Hash) {
		tx.Hash = ""
	}
	if tx.Value == "0" {
		tx.Value = ""
	}
	if in, ok := hexData(t.Input); ok && len(in) >= 4 && tx.To != "" {
		tx.Selector = cutHex(in[:4])
	}
	return tx
}

// evmReceipts fills each transaction's status, gas used and created
// contract, in batches the relay admits.
func (m *Manager) evmReceipts(ctx context.Context, c evmClient, txs []EVMTx) error {
	for start := 0; start < len(txs); start += edge.EVMMaxBatch {
		end := min(start+edge.EVMMaxBatch, len(txs))
		calls := make([]evmReq, 0, end-start)
		for _, tx := range txs[start:end] {
			calls = append(calls, evmReq{Method: "eth_getTransactionReceipt", Params: []any{tx.Hash}})
		}
		raws, err := c.batch(ctx, calls)
		if err != nil {
			return err
		}
		for i, raw := range raws {
			var r *rawReceipt
			if json.Unmarshal(raw, &r) != nil || r == nil {
				continue
			}
			tx := &txs[start+i]
			tx.Status, tx.GasUsed, tx.Contract = r.status(), hexUint(r.GasUsed), lowerHex(r.ContractAddress)
		}
	}
	return nil
}

// EVMTrace replays one transaction of a ready run's EVM service under the
// call tracer and returns its call tree with decoded revert reasons.
func (m *Manager) EVMTrace(ctx context.Context, id, service, tx string) (EVMTrace, error) {
	if !txHashRe.MatchString(tx) {
		return EVMTrace{}, paramsError("tx must be a transaction hash: 0x and 64 hex digits")
	}
	service, c, err := m.evmTarget(id, service)
	if err != nil {
		return EVMTrace{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, EVMTimeout)
	defer cancel()
	receipt, err := c.batch(ctx, []evmReq{{Method: "eth_getTransactionReceipt", Params: []any{tx}}})
	if err != nil {
		return EVMTrace{}, err
	}
	var r *rawReceipt
	if json.Unmarshal(receipt[0], &r) != nil || r == nil {
		return EVMTrace{}, paramsError(fmt.Sprintf("%s has no mined transaction %s", service, tx))
	}
	tracer := map[string]any{"tracer": "callTracer", "tracerConfig": map[string]any{"onlyTopCall": false, "withLog": false}}
	raws, err := c.batch(ctx, []evmReq{{Method: "debug_traceTransaction", Params: []any{tx, tracer}}})
	if err != nil {
		return EVMTrace{}, err
	}
	root, frames, truncated, err := ParseCallTrace(raws[0])
	if err != nil {
		return EVMTrace{}, fmt.Errorf("evm node: %w", err)
	}
	out := EVMTrace{
		Run: id, Service: service, Tx: tx, Block: hexUint(r.BlockNumber), Status: r.status(),
		GasUsed: hexUint(r.GasUsed), Contract: lowerHex(r.ContractAddress),
		Root: root, Frames: frames, Truncated: truncated,
	}
	if root.Revert != nil {
		out.Revert = root.Revert
	}
	return out, nil
}
