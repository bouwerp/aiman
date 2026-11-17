package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/bouwerp/aiman/internal/harness/edge"
)

// runHarnessEdge is the hidden `aiman harness edge`: the relay process inside a
// harness run's edge container. It is started by the run's compose file, never
// by hand.
func runHarnessEdge(args []string) error {
	opts, err := parseEdgeFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, harnessEdgeUsage)
		return errUsage
	}
	return serveEdge(opts)
}

const harnessEdgeUsage = "Usage: aiman harness edge --control <socket> --token-file <file> --outer <addr|prefix> [--inner <prefix>[,<prefix>]]... [--relay name=port=host:port]... [--dap-relay name=port=host:port]... [--evm-relay name=port=host:port]..."

type edgeOptions struct {
	control   string
	tokenFile string
	outer     string
	inner     []netip.Prefix
	relays    []edge.RelaySpec
}

func parseEdgeFlags(args []string) (edgeOptions, error) {
	var opts edgeOptions
	fs := flag.NewFlagSet("harness edge", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.control, "control", "", "control socket path")
	fs.StringVar(&opts.tokenFile, "token-file", "", "file holding the control token")
	fs.StringVar(&opts.outer, "outer", "", "outer address, or the outer network prefix")
	fs.Func("inner", "inner network prefix (repeatable, comma separated)", func(v string) error {
		for _, part := range strings.Split(v, ",") {
			p, err := netip.ParsePrefix(strings.TrimSpace(part))
			if err != nil {
				return err
			}
			opts.inner = append(opts.inner, p)
		}
		return nil
	})
	fs.Func("relay", "relay as name=port=host:port (repeatable)", func(v string) error {
		spec, err := parseEdgeRelay(v)
		if err != nil {
			return err
		}
		opts.relays = append(opts.relays, spec)
		return nil
	})
	fs.Func("dap-relay", "debugger relay as name=port=host:port, one DAP client at a time (repeatable)", func(v string) error {
		spec, err := parseEdgeRelay(v)
		if err != nil {
			return err
		}
		spec.Kind = edge.KindDAP
		opts.relays = append(opts.relays, spec)
		return nil
	})
	fs.Func("evm-relay", "read-only EVM JSON-RPC relay as name=port=host:port (repeatable)", func(v string) error {
		spec, err := parseEdgeRelay(v)
		if err != nil {
			return err
		}
		spec.Kind = edge.KindEVM
		opts.relays = append(opts.relays, spec)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return edgeOptions{}, err
	}
	if fs.NArg() != 0 {
		return edgeOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if opts.control == "" || opts.tokenFile == "" || opts.outer == "" {
		return edgeOptions{}, errors.New("--control, --token-file and --outer are required")
	}
	return opts, nil
}

// parseEdgeRelay reads name=port=host:port. Port 0 picks a free port.
func parseEdgeRelay(v string) (edge.RelaySpec, error) {
	parts := strings.SplitN(v, "=", 3)
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return edge.RelaySpec{}, fmt.Errorf("relay %q must be name=port=host:port", v)
	}
	port, err := strconv.Atoi(parts[1])
	if err != nil || port < 0 || port > 65535 {
		return edge.RelaySpec{}, fmt.Errorf("relay %q has a bad port", v)
	}
	return edge.RelaySpec{Name: parts[0], Port: port, Target: parts[2]}, nil
}

func serveEdge(opts edgeOptions) error {
	token, err := edge.ReadTokenFile(opts.tokenFile)
	if err != nil {
		return err
	}
	outer, err := edge.ResolveOuter(opts.outer, nil)
	if err != nil {
		return err
	}
	e, err := edge.New(edge.Config{Outer: outer, Inner: opts.inner, Token: token})
	if err != nil {
		return err
	}
	defer func() { _ = e.Close() }()
	for _, spec := range opts.relays {
		st, err := e.Add(spec)
		if err != nil {
			return fmt.Errorf("relay %s: %w", spec.Name, err)
		}
		log.Printf("edge: relay %s %s -> %s", st.Name, st.Listen, st.Target)
	}
	ln, err := edge.ListenControl(opts.control)
	if err != nil {
		return err
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)
	go func() {
		<-sigs
		_ = ln.Close()
	}()
	log.Printf("edge: outer %s, control %s", outer, opts.control)
	return e.ServeControl(ln)
}
