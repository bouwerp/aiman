package server

import (
	"testing"

	"github.com/bouwerp/aiman/internal/harness"
)

// TestHarnessEVMRouting checks dispatch, scope and refusals of the EVM
// trace methods; the reads themselves are covered in internal/harness.
func TestHarnessEVMRouting(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	for _, m := range []string{"harness.evm_txs", "harness.evm_trace"} {
		wantHarnessCode(t, m+" no run", rpc(t, srv, "s1", m, map[string]any{"session": "s1"}), harness.CodeNotRunning)
	}
	run := harnessUp(t, srv, "s1", map[string]any{"session": "s1", "scenario": "plain", "wait": true})
	wantHarnessCode(t, "no evm service", rpc(t, srv, "s1", "harness.evm_txs", map[string]any{"run": run.ID}), CodeInvalidParams)
	wantHarnessCode(t, "bad hash", rpc(t, srv, "s1", "harness.evm_trace", map[string]any{"run": run.ID, "tx": "0x1"}), CodeInvalidParams)
	wantHarnessCode(t, "other session", rpc(t, srv, "s2", "harness.evm_txs", map[string]any{"run": run.ID}), CodeSessionScope)
	wantHarnessCode(t, "bad params", rpc(t, srv, "s1", "harness.evm_txs", map[string]any{"run": run.ID, "blocks": "x"}), CodeInvalidParams)
}
