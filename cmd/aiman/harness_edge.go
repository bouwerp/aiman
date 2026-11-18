package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
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

const harnessEdgeUsage = "Usage: aiman harness edge --control <socket> --token-file <file> --outer <addr|prefix> [--inner <prefix>[,<prefix>]]... [--relay name=port=host:port]... [--dap-relay name=port=host:port]... [--evm-relay name=port=host:port]... [--aws-tap name=host:port] [--aws-relay name=port=host:port --aws-region <region> [--aws-download-max N]] [--proxy <inner prefix> --network offline|online --ca-dir <dir> [--stubs <file>] [--capture-body-bytes N] [--upstream-roots <file>] [--block-host-suffix <suffix>]...]"

type edgeOptions struct {
	control   string
	tokenFile string
	outer     string
	inner     []netip.Prefix
	relays    []edge.RelaySpec
	proxy     proxyFlags
	// awsTaps are name=host:port taps to aws emulators.
	awsTaps []edge.RelaySpec
	// awsRegion and awsDownloadMax configure every aws read relay.
	awsRegion      string
	awsDownloadMax int64
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
	fs.Func("dap-relay", "debugger relay as name=port=host:port, one DAP client at a time; name=port= has no target until serve sets one (repeatable)", func(v string) error {
		spec, err := parseRelaySpec(v, true)
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
	fs.Func("aws-tap", "tap to an AWS emulator as name=host:port, on the edge's inner address at 4566", func(v string) error {
		name, target, ok := strings.Cut(v, "=")
		if !ok || name == "" || target == "" {
			return fmt.Errorf("aws tap %q must be name=host:port", v)
		}
		opts.awsTaps = append(opts.awsTaps, edge.RelaySpec{Name: name, Port: edge.AWSTapPort, Target: target})
		return nil
	})
	fs.Func("aws-relay", "read-only AWS relay as name=port=host:port; needs --aws-region", func(v string) error {
		spec, err := parseEdgeRelay(v)
		if err != nil {
			return err
		}
		spec.Kind = edge.KindAWS
		opts.relays = append(opts.relays, spec)
		return nil
	})
	fs.StringVar(&opts.awsRegion, "aws-region", "", "region the AWS read relay signs for")
	fs.Int64Var(&opts.awsDownloadMax, "aws-download-max", 0, "cap on one S3 object body through the AWS read relay, in bytes")
	opts.proxy.register(fs)
	if err := fs.Parse(args); err != nil {
		return edgeOptions{}, err
	}
	if fs.NArg() != 0 {
		return edgeOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if opts.control == "" || opts.tokenFile == "" || opts.outer == "" {
		return edgeOptions{}, errors.New("--control, --token-file and --outer are required")
	}
	if err := opts.proxy.check(); err != nil {
		return edgeOptions{}, err
	}
	hasAWSRelay := false
	for i := range opts.relays {
		if opts.relays[i].Kind == edge.KindAWS {
			hasAWSRelay = true
			opts.relays[i].Region, opts.relays[i].DownloadMax = opts.awsRegion, opts.awsDownloadMax
		}
	}
	if hasAWSRelay && opts.awsRegion == "" {
		return edgeOptions{}, errors.New("--aws-relay needs --aws-region")
	}
	if opts.awsDownloadMax < 0 {
		return edgeOptions{}, errors.New("--aws-download-max must not be negative")
	}
	if len(opts.awsTaps) > 1 {
		return edgeOptions{}, errors.New("--aws-tap is given once: a run has at most one aws service")
	}
	if len(opts.awsTaps) > 0 && len(opts.inner) == 0 {
		return edgeOptions{}, errors.New("--aws-tap needs --inner")
	}
	return opts, nil
}

// parseEdgeRelay reads name=port=host:port. Port 0 picks a free port.
func parseEdgeRelay(v string) (edge.RelaySpec, error) {
	return parseRelaySpec(v, false)
}

// parseRelaySpec reads name=port=host:port; emptyTarget also takes
// name=port=, a debugger relay with no target yet.
func parseRelaySpec(v string, emptyTarget bool) (edge.RelaySpec, error) {
	parts := strings.SplitN(v, "=", 3)
	if len(parts) != 3 || parts[0] == "" || (parts[2] == "" && !emptyTarget) {
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
	if err := startEdgeProxy(e, opts.proxy); err != nil {
		return err
	}
	for _, t := range opts.awsTaps {
		stop, err := startAWSTap(e, opts.inner[0], t)
		if err != nil {
			return fmt.Errorf("aws tap %s: %w", t.Name, err)
		}
		defer stop()
	}
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

// startAWSTap serves the tap on the edge's address in the inner prefix,
// where the aws service's name points, and hands it to e so the control
// socket reads its call log.
func startAWSTap(e *edge.Edge, inner netip.Prefix, spec edge.RelaySpec) (func(), error) {
	tap, err := edge.NewAWSTap(spec.Target, edge.WithAWSTapName(spec.Name))
	if err != nil {
		return nil, err
	}
	addr, err := edge.ResolveOuter(inner.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("tap address: %w", err)
	}
	ln, err := net.Listen("tcp4", net.JoinHostPort(addr.String(), strconv.Itoa(spec.Port)))
	if err != nil {
		return nil, fmt.Errorf("tap listen: %w", err)
	}
	go func() {
		if err := tap.Serve(ln); err != nil {
			log.Printf("edge: aws tap %s stopped: %v", spec.Name, err)
		}
	}()
	e.SetAWSTap(tap)
	log.Printf("edge: aws tap %s %s -> %s", spec.Name, ln.Addr(), spec.Target)
	return func() { _ = tap.Close() }, nil
}
