package harness

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode/utf8"
)

// Parse bounds for a call tracer answer. The chain runs code the tester did
// not write, so the tree is bounded before it reaches the console.
const (
	// MaxEVMCalls caps the call frames a trace keeps; the rest are dropped
	// and the trace says truncated.
	MaxEVMCalls = 5000
	// maxEVMDepth caps nesting; the EVM's own limit is 1024.
	maxEVMDepth = 1100
	// maxEVMHexBytes caps the bytes of input, output or revert data a frame
	// keeps; the full length is reported beside it.
	maxEVMHexBytes = 4096
	// maxRevertMessage caps a decoded Error(string) message.
	maxRevertMessage = 2048
)

// Revert kinds.
const (
	RevertError  = "error"  // Error(string)
	RevertPanic  = "panic"  // Panic(uint256)
	RevertCustom = "custom" // a custom error: a 4-byte selector and its arguments
	RevertRaw    = "raw"    // data too short to carry a selector
	RevertEmpty  = "empty"  // no data at all
)

// Revert selectors.
const (
	selectorError = "08c379a0"
	selectorPanic = "4e487b71"
)

// panicReasons are Solidity's Panic(uint256) codes.
var panicReasons = map[uint64]string{
	0x00: "generic compiler panic",
	0x01: "assert failed",
	0x11: "arithmetic overflow or underflow",
	0x12: "division or modulo by zero",
	0x21: "conversion to an enum out of range",
	0x22: "incorrectly encoded storage byte array",
	0x31: "pop on an empty array",
	0x32: "array index out of bounds",
	0x41: "too much memory allocated",
	0x51: "call to a zero-initialized function variable",
}

// EVMRevert is a decoded revert reason. Message is the Error(string) text or
// the panic's meaning; Code the panic code; Selector a custom error's
// selector; Data the raw revert data in hex (cut at 4096 bytes).
type EVMRevert struct {
	Kind     string `json:"kind"`
	Message  string `json:"message,omitempty"`
	Code     string `json:"code,omitempty"`
	Selector string `json:"selector,omitempty"`
	Data     string `json:"data,omitempty"`
	Bytes    int    `json:"bytes"`
}

// EVMCall is one call frame of a trace. Value is in wei, in decimal. Input
// and Output are hex cut at 4096 bytes; InputBytes and OutputBytes are the
// full lengths. Error is the node's word for a failed frame ("execution
// reverted", "out of gas"); Revert decodes its output.
type EVMCall struct {
	Type        string     `json:"type"`
	From        string     `json:"from"`
	To          string     `json:"to,omitempty"`
	Value       string     `json:"value,omitempty"`
	Gas         uint64     `json:"gas"`
	GasUsed     uint64     `json:"gas_used"`
	Selector    string     `json:"selector,omitempty"`
	Input       string     `json:"input,omitempty"`
	InputBytes  int        `json:"input_bytes"`
	Output      string     `json:"output,omitempty"`
	OutputBytes int        `json:"output_bytes"`
	Error       string     `json:"error,omitempty"`
	Revert      *EVMRevert `json:"revert,omitempty"`
	Calls       []EVMCall  `json:"calls,omitempty"`
}

// rawCallFrame is geth's callTracer frame, which anvil also answers with.
type rawCallFrame struct {
	Type         string            `json:"type"`
	From         string            `json:"from"`
	To           string            `json:"to"`
	Value        string            `json:"value"`
	Gas          string            `json:"gas"`
	GasUsed      string            `json:"gasUsed"`
	Input        string            `json:"input"`
	Output       string            `json:"output"`
	Error        string            `json:"error"`
	RevertReason string            `json:"revertReason"`
	Calls        []json.RawMessage `json:"calls"`
}

// ParseCallTrace turns a callTracer answer into a call tree with decoded
// revert reasons. It keeps at most MaxEVMCalls frames in depth-first order
// and reports whether it dropped any.
func ParseCallTrace(raw []byte) (root *EVMCall, frames int, truncated bool, err error) {
	p := &traceParser{}
	c, err := p.frame(raw, 0)
	if err != nil {
		return nil, 0, false, err
	}
	return &c, p.n, p.truncated, nil
}

type traceParser struct {
	n         int
	truncated bool
}

func (p *traceParser) frame(raw []byte, depth int) (EVMCall, error) {
	var f rawCallFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		return EVMCall{}, fmt.Errorf("parse call frame: %w", err)
	}
	if f.Type == "" && f.From == "" {
		return EVMCall{}, errors.New("parse call frame: not a callTracer frame")
	}
	p.n++
	in, inOK := hexData(f.Input)
	out, outOK := hexData(f.Output)
	c := EVMCall{
		Type: strings.ToUpper(f.Type), From: lowerHex(f.From), To: lowerHex(f.To),
		Value: hexDecimal(f.Value), Gas: hexUint(f.Gas), GasUsed: hexUint(f.GasUsed),
		Input: cutHex(in), InputBytes: len(in), Output: cutHex(out), OutputBytes: len(out),
		Error: oneLine(f.Error, 200),
	}
	if !inOK {
		c.Input, c.InputBytes = "", 0
	}
	if !outOK {
		c.Output, c.OutputBytes = "", 0
	}
	if len(in) >= 4 && !strings.HasPrefix(c.Type, "CREATE") {
		c.Selector = "0x" + hex.EncodeToString(in[:4])
	}
	if c.Value == "0" {
		c.Value = ""
	}
	if c.Error != "" {
		c.Revert = DecodeRevert(out)
		if c.Revert.Kind == RevertEmpty && f.RevertReason != "" {
			c.Revert.Kind, c.Revert.Message = RevertError, oneLine(f.RevertReason, maxRevertMessage)
		}
	}
	for _, sub := range f.Calls {
		if depth+1 > maxEVMDepth || p.n >= MaxEVMCalls {
			p.truncated = true
			break
		}
		child, err := p.frame(sub, depth+1)
		if err != nil {
			return EVMCall{}, err
		}
		c.Calls = append(c.Calls, child)
	}
	return c, nil
}

// DecodeRevert reads revert data: Error(string), Panic(uint256), a custom
// error's selector, or raw bytes.
func DecodeRevert(data []byte) *EVMRevert {
	r := &EVMRevert{Bytes: len(data), Data: cutHex(data)}
	switch {
	case len(data) == 0:
		r.Kind, r.Data = RevertEmpty, ""
		return r
	case len(data) < 4:
		r.Kind = RevertRaw
		return r
	}
	sel := hex.EncodeToString(data[:4])
	args := data[4:]
	switch sel {
	case selectorError:
		if msg, ok := abiString(args); ok {
			r.Kind, r.Message = RevertError, msg
			return r
		}
	case selectorPanic:
		if len(args) == 32 {
			code := new(big.Int).SetBytes(args)
			r.Kind, r.Code = RevertPanic, "0x"+code.Text(16)
			r.Message = "unknown panic code"
			if code.IsUint64() {
				if m, ok := panicReasons[code.Uint64()]; ok {
					r.Message = m
				}
			}
			return r
		}
	}
	r.Kind, r.Selector = RevertCustom, "0x"+sel
	return r
}

// abiString decodes one ABI-encoded string argument: a 32-byte offset, then
// at that offset a 32-byte length and the bytes.
func abiString(args []byte) (string, bool) {
	if len(args) < 64 {
		return "", false
	}
	off, ok := abiWord(args[:32])
	if !ok || off > uint64(len(args))-32 {
		return "", false
	}
	n, ok := abiWord(args[off : off+32])
	if !ok || n > uint64(len(args))-off-32 {
		return "", false
	}
	s := args[off+32 : off+32+n]
	msg := string(s)
	if !utf8.ValidString(msg) {
		msg = strings.ToValidUTF8(msg, "�")
	}
	return oneLine(msg, maxRevertMessage), true
}

// abiWord reads a 32-byte big-endian word that fits in a uint64.
func abiWord(w []byte) (uint64, bool) {
	v := new(big.Int).SetBytes(w)
	if !v.IsUint64() {
		return 0, false
	}
	return v.Uint64(), true
}

// hexData decodes a 0x-prefixed hex string; empty or "0x" is no data, and
// anything else that is not hex reports false.
func hexData(s string) ([]byte, bool) {
	s = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "0x"), "0X")
	if s == "" {
		return nil, true
	}
	if len(s)%2 == 1 {
		s = "0" + s
	}
	b, err := hex.DecodeString(s)
	return b, err == nil
}

func cutHex(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if len(b) > maxEVMHexBytes {
		b = b[:maxEVMHexBytes]
	}
	return "0x" + hex.EncodeToString(b)
}

// hexBig parses a hex quantity; nil when it is not one.
func hexBig(s string) *big.Int {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	v, ok := new(big.Int).SetString(strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X"), 16)
	if !ok || v.Sign() < 0 {
		return nil
	}
	return v
}

func hexUint(s string) uint64 {
	v := hexBig(s)
	if v == nil || !v.IsUint64() {
		return 0
	}
	return v.Uint64()
}

func hexDecimal(s string) string {
	v := hexBig(s)
	if v == nil {
		return ""
	}
	return v.String()
}

// lowerHex keeps an address only when it is 0x and 40 hex digits.
func lowerHex(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 42 || !strings.HasPrefix(s, "0x") {
		return ""
	}
	if _, err := hex.DecodeString(s[2:]); err != nil {
		return ""
	}
	return s
}

// oneLine keeps printable text on one line, cut at n bytes.
func oneLine(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "") + "…"
	}
	return s
}
