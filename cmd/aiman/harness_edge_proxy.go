package main

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/bouwerp/aiman/internal/harness/edge"
)

// proxyFlags are the edge's capture proxy flags.
type proxyFlags struct {
	// prefix is the inner network; the proxy binds the edge's address in it.
	prefix    string
	network   string
	caDir     string
	stubs     string
	roots     string
	bodyBytes int
}

func (p *proxyFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&p.prefix, "proxy", "", "run the capture proxy on the edge's address in this inner prefix")
	fs.StringVar(&p.network, "network", "offline", "offline or online")
	fs.StringVar(&p.caDir, "ca-dir", "", "directory the run CA's certificate is written to")
	fs.StringVar(&p.stubs, "stubs", "", "JSON file of the scenario's stubs")
	fs.StringVar(&p.roots, "upstream-roots", "", "CA bundle that verifies upstream servers online")
	fs.IntVar(&p.bodyBytes, "capture-body-bytes", 0, "bytes of each body a flow keeps")
}

func (p proxyFlags) check() error {
	if p.prefix == "" {
		if p.caDir != "" || p.stubs != "" || p.roots != "" || p.bodyBytes != 0 || p.network != "offline" {
			return errors.New("proxy flags need --proxy")
		}
		return nil
	}
	if p.network != "offline" && p.network != "online" {
		return fmt.Errorf("--network %q must be offline or online", p.network)
	}
	if p.caDir == "" {
		return errors.New("--proxy needs --ca-dir")
	}
	if p.network == "online" && p.roots == "" {
		return errors.New("--network online needs --upstream-roots")
	}
	if p.bodyBytes < 0 {
		return errors.New("--capture-body-bytes must not be negative")
	}
	return nil
}

// startEdgeProxy makes the run's CA in memory, writes only its certificate
// to the CA directory, and serves the capture proxy on the edge's inner
// address. With no --proxy it does nothing.
func startEdgeProxy(e *edge.Edge, p proxyFlags) error {
	if p.prefix == "" {
		return nil
	}
	ca, err := edge.NewCA(time.Now())
	if err != nil {
		return err
	}
	if err := ca.WriteCert(p.caDir); err != nil {
		return err
	}
	var stubs []edge.Stub
	if p.stubs != "" {
		b, err := os.ReadFile(p.stubs)
		if err != nil {
			return fmt.Errorf("read stubs: %w", err)
		}
		if err := json.Unmarshal(b, &stubs); err != nil {
			return fmt.Errorf("parse stubs: %w", err)
		}
	}
	cfg := edge.ProxyConfig{Online: p.network == "online", CA: ca, Stubs: stubs, BodyBytes: p.bodyBytes}
	if cfg.Online {
		pem, err := os.ReadFile(p.roots)
		if err != nil {
			return fmt.Errorf("read upstream roots: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return fmt.Errorf("upstream roots %s hold no certificate", p.roots)
		}
		cfg.RootCAs = pool
	}
	proxy, err := edge.NewProxy(cfg)
	if err != nil {
		return err
	}
	addr, err := edge.ResolveOuter(p.prefix, nil)
	if err != nil {
		return fmt.Errorf("proxy address: %w", err)
	}
	ln, err := net.Listen("tcp4", net.JoinHostPort(addr.String(), strconv.Itoa(edge.ProxyPort)))
	if err != nil {
		return fmt.Errorf("proxy listen: %w", err)
	}
	e.SetProxy(proxy)
	go func() {
		if err := proxy.Serve(ln); err != nil {
			log.Printf("edge: proxy stopped: %v", err)
		}
	}()
	log.Printf("edge: proxy %s (%s), %d stubs", ln.Addr(), p.network, len(stubs))
	return nil
}
