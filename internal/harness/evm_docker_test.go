package harness

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// anvilImage is Foundry v1.8.1 pinned by digest (linux/amd64 index digest
// as the registry served it on 2026-11-17).
const anvilImage = "ghcr.io/foundry-rs/foundry@sha256:0c00cb0bda1ab1b91c9a6bf60f4c76c09c1a8870824b6d4718afbabacf6f9a17"

// Anvil's first dev account and the addresses its first two creations get.
const (
	anvilDev0   = "0xf39fd6e51aad88f6f4ce6ab8827279cfffb92266"
	anvilFirst  = "5fbdb2315678afecb367f032d93f642f64180aa3"
	anvilSecond = "0xe7f1725e7734ce288f8367e1bb143e90bb3f0512"
)

// deployCode wraps runtime code in init code that returns it.
func deployCode(runtime []byte) string {
	init := []byte{0x60, byte(len(runtime)), 0x80, 0x60, 0x0b, 0x60, 0x00, 0x39, 0x60, 0x00, 0xf3}
	return "0x" + hex.EncodeToString(append(init, runtime...))
}

// evmContracts are a reverter, whose runtime reverts with Error("nope"), and
// a caller, whose runtime calls the reverter and re-raises its revert data.
func evmContracts(t *testing.T) (reverter, caller string) {
	t.Helper()
	data, ok := hexData(errorData("nope"))
	if !ok || len(data) != 100 {
		t.Fatalf("error data %d bytes", len(data))
	}
	// PUSH1 100 PUSH1 12 PUSH1 0 CODECOPY PUSH1 100 PUSH1 0 REVERT, then data.
	r := append([]byte{0x60, 0x64, 0x60, 0x0c, 0x60, 0x00, 0x39, 0x60, 0x64, 0x60, 0x00, 0xfd}, data...)
	addr, _ := hex.DecodeString(anvilFirst)
	// CALL(gas, reverter, 0, 0, 0, 0, 0), RETURNDATACOPY(0, 0, size), REVERT(0, size).
	c := []byte{0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x73}
	c = append(c, addr...)
	c = append(c, 0x5a, 0xf1, 0x3d, 0x60, 0x00, 0x60, 0x00, 0x3e, 0x3d, 0x60, 0x00, 0xfd)
	return deployCode(r), deployCode(c)
}

func sendTx(params string) string {
	return fmt.Sprintf(`wget -q -O- --header 'Content-Type: application/json' --post-data '{"jsonrpc":"2.0","id":1,"method":"eth_sendTransaction","params":[%s]}' http://chain:8545/ && echo`, params)
}

// TestEVMDockerTrace runs a real anvil service on the offline inner network,
// sends transactions from a setup step, and reads them and a reverted call's
// tree through the edge's read-only relay. A write through the relay is
// refused.
func TestEVMDockerTrace(t *testing.T) {
	needDocker(t)
	ensureImage(t, dockerTestImage)
	ensureImage(t, EdgeImage)
	ensureImage(t, anvilImage)
	reverter, caller := evmContracts(t)
	from := `"from":"` + anvilDev0 + `"`
	setup := []string{
		sendTx(`{` + from + `,"data":"` + reverter + `"}`),
		sendTx(`{` + from + `,"data":"` + caller + `"}`),
		sendTx(`{` + from + `,"to":"` + anvilSecond + `","gas":"0x100000","data":"0x12345678"}`),
	}
	var file bytes.Buffer
	file.WriteString("version: 1\napp:\n  run: exec sleep 3600\nservices:\n  chain:\n    image: " + anvilImage + "\n    evm: {kind: anvil}\nscenarios:\n  - id: chain\n    services: [chain]\n    setup:\n")
	for _, s := range setup {
		file.WriteString("      - run: |\n          " + s + "\n")
	}
	repo := testRepo(t, file.String())
	m, err := NewManager(Options{
		Dir:      filepath.Join(t.TempDir(), "harness"),
		AimanBin: edgeBinary(t),
		UID:      os.Getuid(), GID: os.Getgid(),
		AllowedImages: []string{"busybox:*", anvilImage},
		Docker:        ExecDocker{},
		AppImage:      func(string, string) (string, error) { return dockerTestImage, nil },
		Record:        (&recorder{}).record,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	run, err := m.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "chain", Actor: domain.EventActorUser})
	if err != nil {
		t.Fatal(err)
	}
	project := "harness-" + run.ID
	t.Cleanup(func() {
		_ = exec.Command("docker", "compose", "-p", project, "down", "-v", "--remove-orphans", "-t", "1").Run()
	})
	run = m.Wait(context.Background(), run.ID, 4*time.Minute)
	if run.State != StateReady {
		t.Fatalf("run %s: %s: %s", run.State, run.FailedStep, run.Error)
	}
	if len(run.EVM) != 1 || run.EVM[0] != "chain" {
		t.Fatalf("run evm %v", run.EVM)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	list, err := m.EVMTxs(ctx, run.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if list.ChainID != 31337 || len(list.Txs) != 3 {
		t.Fatalf("list %+v", list)
	}
	failed := list.Txs[0]
	if failed.Status != EVMStatusReverted || failed.To != anvilSecond || failed.Selector != "0x12345678" {
		t.Fatalf("newest tx %+v", failed)
	}
	if list.Txs[2].Status != EVMStatusSuccess || list.Txs[2].Contract != "0x"+anvilFirst {
		t.Fatalf("first tx %+v", list.Txs[2])
	}
	tr, err := m.EVMTrace(ctx, run.ID, "chain", failed.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != EVMStatusReverted || tr.Revert == nil || tr.Revert.Kind != RevertError || tr.Revert.Message != "nope" {
		t.Fatalf("trace %+v revert %+v", tr, tr.Revert)
	}
	if tr.Root == nil || len(tr.Root.Calls) != 1 || tr.Root.Calls[0].To != "0x"+anvilFirst ||
		tr.Root.Calls[0].Revert == nil || tr.Root.Calls[0].Revert.Message != "nope" {
		t.Fatalf("call tree %+v", tr.Root)
	}

	// The relay the host sees refuses a write: no fourth transaction appears.
	port := 0
	for _, p := range run.Ports {
		if p.Name == "chain" {
			port = p.HostPort
		}
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"eth_sendTransaction","params":[{` + from + `,"to":"` + anvilSecond + `","gas":"0x100000"}]}`
	resp, err := http.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("write through the relay: HTTP %d", resp.StatusCode)
	}
	if again, err := m.EVMTxs(ctx, run.ID, "", 0); err != nil || len(again.Txs) != 3 {
		t.Fatalf("after the refused write: %v %+v", err, again)
	}
	if err := m.Down(context.Background(), run.ID, StopDown); err != nil {
		t.Fatal(err)
	}
}
