package harness

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// EVM services (P9): an anvil node on the run's offline inner network whose
// transactions the console traces. Serve and the console only read it: the
// edge relays its port as a JSON-RPC filter that admits edge.EVMReadMethods
// and nothing else, so no signing or state-changing call leaves the sandbox.
// The app and setup scripts reach the node directly on the inner network and
// may send transactions.

// EVM kinds.
const EVMKindAnvil = "anvil"

// RelayKindEVM marks the relay of an EVM service: a read-only JSON-RPC
// filter in the edge.
const RelayKindEVM = "evm"

// DefaultEVMPort is the port anvil listens on in its container.
const DefaultEVMPort = 8545

// maxEVMArgs and maxEVMArgBytes bound evm.args.
const (
	maxEVMArgs     = 32
	maxEVMArgBytes = 256
)

// EVM declares a service as an EVM node. Kind is anvil. Port is the port it
// listens on (default 8545); ChainID sets --chain-id; Args are more anvil
// flags. aiman sets the entrypoint to anvil with --host and --port.
type EVM struct {
	Kind    string   `yaml:"kind" json:"kind"`
	Port    int      `yaml:"port" json:"port,omitempty"`
	ChainID uint64   `yaml:"chain_id" json:"chain_id,omitempty"`
	Args    []string `yaml:"args" json:"args,omitempty"`
}

func (e *EVM) port() int {
	if e.Port == 0 {
		return DefaultEVMPort
	}
	return e.Port
}

// evmForkFlags need a remote node and so network egress, which an offline
// run never has.
var evmForkFlags = []string{"--fork-url", "-f", "--rpc-url", "--fork-block-number", "--fork-chain-id",
	"--fork-header", "--fork-retry-backoff", "--fork-transaction-hash", "--compute-units-per-second",
	"--no-rate-limit", "--no-storage-caching", "--retries", "--timeout"}

// evmOwnFlags are set by aiman, or open another way in.
var evmOwnFlags = []string{"--host", "--port", "-p", "--ipc", "--chain-id", "--config-out", "--dump-state",
	"--load-state", "--state", "--state-interval", "--cache-path", "--allow-origin"}

func validateEVM(at string, s Service) []string {
	e := s.EVM
	if e == nil {
		return nil
	}
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, at+": "+fmt.Sprintf(format, args...)) }
	if e.Kind != EVMKindAnvil {
		add("evm.kind %q must be %s", e.Kind, EVMKindAnvil)
	}
	if s.Database != nil {
		add("a service is an evm node or a database, not both")
	}
	if e.Port < 0 || e.Port > 65535 {
		add("evm.port %d is not 1-65535", e.Port)
	}
	if len(e.Args) > maxEVMArgs {
		add("evm.args has %d entries; at most %d", len(e.Args), maxEVMArgs)
	}
	for i, a := range e.Args {
		flag, _, _ := strings.Cut(strings.TrimSpace(a), "=")
		switch {
		case len(a) > maxEVMArgBytes || strings.ContainsAny(a, "\n\r\x00"):
			add("evm.args[%d] must be one line of at most %d bytes", i, maxEVMArgBytes)
		case slices.Contains(evmForkFlags, flag):
			add("evm.args[%d]: %s forks a remote chain, which needs network egress; an offline run has none", i, flag)
		case slices.Contains(evmOwnFlags, flag):
			add("evm.args[%d]: %s is set by aiman or not allowed", i, flag)
		}
	}
	return errs
}

// EVMServices lists the scenario's EVM services in scenario order.
func (f *File) EVMServices(sc *Scenario) []string {
	var out []string
	for _, name := range sc.Services {
		if f.Services[name].EVM != nil {
			out = append(out, name)
		}
	}
	return out
}

// evmService runs anvil as the service: listening on every address of the
// inner network (the only one it joins), HOME on the tmpfs because the root
// is read-only, and a ready probe through cast unless the file names one.
func evmService(s *composeService, svc Service) {
	e := svc.EVM
	port := strconv.Itoa(e.port())
	s.Entrypoint = []string{"anvil"}
	cmd := []string{"--host", "0.0.0.0", "--port", port}
	if e.ChainID != 0 {
		cmd = append(cmd, "--chain-id", strconv.FormatUint(e.ChainID, 10))
	}
	cmd = append(cmd, e.Args...)
	s.Command = cmd
	if _, ok := s.Environment["HOME"]; !ok {
		s.Environment["HOME"] = "/tmp"
	}
	if svc.Ready == "" {
		s.Healthcheck = &composeHealthcheck{
			Test:     []string{"CMD-SHELL", "cast block-number --rpc-url http://127.0.0.1:" + port + " >/dev/null"},
			Interval: "1s", Timeout: "5s", Retries: 60,
		}
	}
}
