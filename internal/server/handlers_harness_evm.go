package server

import (
	"context"
	"encoding/json"
	"strings"
)

// harnessEVMParams are harness.evm_txs and harness.evm_trace's parameters.
type harnessEVMParams struct {
	Session string `json:"session"`
	Run     string `json:"run"`
	Service string `json:"service"`
	Tx      string `json:"tx"`
	Blocks  int    `json:"blocks"`
}

// dispatchHarnessEVM answers harness.evm_txs {run|session, service?,
// blocks?} and harness.evm_trace {run|session, service?, tx}. Both only read
// the chain, through the edge's read-only relay, so any in-scope caller may
// ask; neither records a ledger row.
func (s *Server) dispatchHarnessEVM(ctx context.Context, req Request) Response {
	var p harnessEVMParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	if strings.TrimSpace(p.Session) == "" {
		p.Session = req.Caller
	}
	run, fail := s.harnessRun(ctx, req, harnessParams{Session: p.Session, Run: p.Run}, false)
	if fail != nil {
		return *fail
	}
	m := s.harness.d.Manager
	m.Touch(run.ID)
	service := strings.TrimSpace(p.Service)
	switch req.Method {
	case "harness.evm_txs":
		list, err := m.EVMTxs(ctx, run.ID, service, p.Blocks)
		if err != nil {
			return harnessErr(req.ID, err, CodeUnavailable)
		}
		return Response{ID: req.ID, Result: map[string]any{"type": "harness_evm_txs", "evm": list}}
	case "harness.evm_trace":
		tr, err := m.EVMTrace(ctx, run.ID, service, strings.TrimSpace(p.Tx))
		if err != nil {
			return harnessErr(req.ID, err, CodeUnavailable)
		}
		return Response{ID: req.ID, Result: map[string]any{"type": "harness_evm_trace", "trace": tr}}
	}
	return errResp(req.ID, CodeInvalidParams, "unknown method "+req.Method)
}
