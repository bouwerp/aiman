package edge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"time"
)

// KindEVM marks a relay in front of an EVM node's JSON-RPC: it admits HTTP
// POSTs whose every call names a method in EVMReadMethods and refuses the
// rest, so the published port can read the chain and never sign, send or
// change state. WebSocket upgrades and every other verb are refused.
const KindEVM = "evm"

// EVMReadMethods are the only JSON-RPC methods an EVM relay passes: the
// console's transaction list and trace, nothing that signs, sends, mines or
// sets state.
var EVMReadMethods = []string{
	"eth_blockNumber",
	"eth_chainId",
	"eth_getBlockByNumber",
	"eth_getTransactionByHash",
	"eth_getTransactionReceipt",
	"debug_traceTransaction",
}

// EVMMethodAllowed reports whether method is in EVMReadMethods.
func EVMMethodAllowed(method string) bool { return slices.Contains(EVMReadMethods, method) }

// EVM relay bounds. The node and the client are both untrusted.
const (
	// EVMMaxRequestBytes caps a request body; the read methods take a few
	// short parameters.
	EVMMaxRequestBytes = 64 << 10
	// EVMMaxBatch caps the calls in one batch request.
	EVMMaxBatch = 64
	// EVMMaxResponseBytes caps the node's answer; a larger one is refused
	// whole rather than cut into invalid JSON.
	EVMMaxResponseBytes = 32 << 20
	// evmMaxHeaderBytes caps a request's line and headers.
	evmMaxHeaderBytes = 16 << 10
	// evmIdle closes a kept-alive connection with no next request.
	evmIdle = 60 * time.Second
	// evmCallTimeout bounds one forwarded request.
	evmCallTimeout = 60 * time.Second
)

// JSON-RPC error codes the relay answers with.
const (
	evmCodeParse    = -32700
	evmCodeInvalid  = -32600
	evmCodeNotFound = -32601
	evmCodeUpstream = -32603
)

// capReader stops a client from sending more than n bytes for one request.
type capReader struct {
	r io.Reader
	n int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.n <= 0 {
		return 0, errors.New("request too large")
	}
	if int64(len(p)) > c.n {
		p = p[:c.n]
	}
	n, err := c.r.Read(p)
	c.n -= int64(n)
	return n, err
}

// handleEVM serves HTTP/1.1 on one client connection, forwarding each
// admitted request to the node.
func (r *relay) handleEVM(client net.Conn) {
	cr := &capReader{r: client}
	br := bufio.NewReader(cr)
	upstream := &http.Client{
		Timeout: evmCallTimeout,
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: r.dialTimeout}
				return d.DialContext(ctx, network, r.spec.Target)
			},
		},
	}
	defer upstream.CloseIdleConnections()
	for {
		cr.n = evmMaxHeaderBytes + EVMMaxRequestBytes
		_ = client.SetReadDeadline(time.Now().Add(evmIdle))
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		status, body := r.evmAnswer(req, upstream)
		_ = req.Body.Close()
		resp := &http.Response{
			StatusCode: status, ProtoMajor: 1, ProtoMinor: 1, Request: req,
			Header:        http.Header{"Content-Type": {"application/json"}},
			ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)),
			Close: req.Close || status != http.StatusOK,
		}
		_ = client.SetWriteDeadline(time.Now().Add(evmCallTimeout))
		if err := resp.Write(client); err != nil || resp.Close {
			return
		}
	}
}

// evmAnswer checks one request and forwards it when every call is a read.
func (r *relay) evmAnswer(req *http.Request, upstream *http.Client) (int, []byte) {
	if req.Method != http.MethodPost {
		r.refused.Add(1)
		return http.StatusMethodNotAllowed, evmError(evmCodeInvalid, "the EVM relay takes JSON-RPC over HTTP POST only")
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, EVMMaxRequestBytes+1))
	if err != nil {
		return http.StatusBadRequest, evmError(evmCodeParse, "read request: "+err.Error())
	}
	if len(raw) > EVMMaxRequestBytes {
		r.refused.Add(1)
		return http.StatusRequestEntityTooLarge, evmError(evmCodeInvalid, "request body over "+strconv.Itoa(EVMMaxRequestBytes)+" bytes")
	}
	body, err := FilterEVMRequest(raw)
	if err != nil {
		r.refused.Add(1)
		code := evmCodeInvalid
		var me *EVMMethodError
		if errors.As(err, &me) {
			code = evmCodeNotFound
		}
		return http.StatusForbidden, evmError(code, err.Error())
	}
	ctx, cancel := context.WithTimeout(r.ctx, evmCallTimeout)
	defer cancel()
	up, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+r.spec.Target+"/", bytes.NewReader(body))
	if err != nil {
		return http.StatusBadGateway, evmError(evmCodeUpstream, err.Error())
	}
	up.Header.Set("Content-Type", "application/json")
	resp, err := upstream.Do(up)
	if err != nil {
		r.failed.Add(1)
		r.setErr(err)
		return http.StatusBadGateway, evmError(evmCodeUpstream, "the EVM node did not answer")
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, EVMMaxResponseBytes+1))
	if err != nil {
		return http.StatusBadGateway, evmError(evmCodeUpstream, "read the EVM node's answer: "+err.Error())
	}
	if len(out) > EVMMaxResponseBytes {
		return http.StatusBadGateway, evmError(evmCodeUpstream, "the EVM node's answer is over "+strconv.Itoa(EVMMaxResponseBytes)+" bytes")
	}
	if !json.Valid(out) {
		return http.StatusBadGateway, evmError(evmCodeUpstream, "the EVM node answered with something other than JSON")
	}
	return http.StatusOK, out
}

// EVMMethodError is a call to a method outside EVMReadMethods.
type EVMMethodError struct{ Method string }

func (e *EVMMethodError) Error() string {
	return fmt.Sprintf("method %q is not allowed through the harness EVM relay; it admits only %v", e.Method, EVMReadMethods)
}

// FilterEVMRequest checks a JSON-RPC request or batch and returns it rebuilt
// from jsonrpc, id, method and params alone, so the node parses exactly what
// was checked (a duplicate method key cannot name a second method).
func FilterEVMRequest(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errors.New("empty request")
	}
	if trimmed[0] != '[' {
		call, err := filterEVMCall(trimmed)
		if err != nil {
			return nil, err
		}
		return json.Marshal(call)
	}
	var batch []json.RawMessage
	if err := json.Unmarshal(trimmed, &batch); err != nil {
		return nil, fmt.Errorf("parse batch: %w", err)
	}
	if len(batch) == 0 || len(batch) > EVMMaxBatch {
		return nil, fmt.Errorf("a batch holds 1 to %d calls, not %d", EVMMaxBatch, len(batch))
	}
	out := make([]evmCall, 0, len(batch))
	for _, item := range batch {
		call, err := filterEVMCall(item)
		if err != nil {
			return nil, err
		}
		out = append(out, call)
	}
	return json.Marshal(out)
}

type evmCall struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func filterEVMCall(raw []byte) (evmCall, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return evmCall{}, errors.New("each call must be a JSON object")
	}
	var method string
	if err := json.Unmarshal(fields["method"], &method); err != nil {
		return evmCall{}, errors.New("each call needs a string method")
	}
	if !EVMMethodAllowed(method) {
		return evmCall{}, &EVMMethodError{Method: method}
	}
	call := evmCall{JSONRPC: "2.0", ID: fields["id"], Method: method}
	if p, ok := fields["params"]; ok {
		t := bytes.TrimSpace(p)
		if len(t) == 0 || (t[0] != '[' && t[0] != '{') {
			return evmCall{}, errors.New("params must be an array or an object")
		}
		call.Params = p
	}
	return call, nil
}

func evmError(code int, msg string) []byte {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": code, "message": msg}})
	return b
}
