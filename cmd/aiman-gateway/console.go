package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/gateway"
	"tailscale.com/client/local"
	"tailscale.com/tsnet"
)

// consolePort is where the shared harness console listens on the tailnet.
const consolePort = 8090

// hostNodesTTL keeps the host's own node ids between status reads.
const hostNodesTTL = 30 * time.Second

// startConsole serves shared harness consoles on :8090 when console logins
// are configured and Funnel is off. It waits for tsnet to come up, so it runs
// beside the main API; errors are logged, never fatal.
func startConsole(ctx context.Context, ts *tsnet.Server, hostname, socket string, logins []string, funnel bool) {
	if len(logins) == 0 {
		return
	}
	if funnel {
		log.Printf("harness console: off under Funnel (gateway.console_logins is ignored)")
		return
	}
	go func() {
		if err := runConsole(ctx, ts, hostname, socket, logins); err != nil && ctx.Err() == nil {
			log.Printf("harness console: %v", err)
		}
	}()
}

func runConsole(ctx context.Context, ts *tsnet.Server, hostname, socket string, logins []string) error {
	st, err := ts.Up(ctx)
	if err != nil {
		return fmt.Errorf("tailnet: %w", err)
	}
	lc, err := ts.LocalClient()
	if err != nil {
		return fmt.Errorf("tailscale local client: %w", err)
	}
	domains := ts.CertDomains()
	hosts := []string{hostname}
	hosts = append(hosts, domains...)
	for _, ip := range st.TailscaleIPs {
		hosts = append(hosts, ip.String())
	}

	addr := fmt.Sprintf(":%d", consolePort)
	scheme, base := "http", "http://"+hostname
	var ln net.Listener
	if len(domains) > 0 {
		scheme, base = "https", "https://"+domains[0]
		ln, err = ts.ListenTLS("tcp", addr)
	} else {
		ln, err = ts.Listen("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	defer ln.Close()

	rpc := gateway.SocketRPC{Socket: socket}
	cs, err := gateway.NewConsoleShares(gateway.ConsoleOptions{
		Logins:    logins,
		Hosts:     hosts,
		Port:      consolePort,
		HTTPS:     scheme == "https",
		WhoIs:     peerWhoIs(lc),
		HostNodes: hostNodes(lc),
		RPC:       rpc,
		Listen:    ts.Listen,
		Log:       os.Stderr,
	})
	if err != nil {
		return err
	}
	defer cs.Close()

	go gateway.RunAnnounce(ctx, rpc, gateway.AnnounceParams{
		Base: base, ConsolePort: consolePort, Scheme: scheme, PID: os.Getpid(),
	}, log.Printf)
	go gateway.WatchShareEvents(ctx, socket, cs)
	go func() { _ = cs.Run(ctx) }()

	srv := &http.Server{Handler: cs.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	log.Printf("harness console listening at %s:%d for %d login(s)", base, consolePort, len(logins))
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// peerWhoIs reads a tailnet connection's login and node StableID.
func peerWhoIs(lc *local.Client) gateway.PeerWhoIs {
	return func(ctx context.Context, remoteAddr string) (gateway.Peer, error) {
		who, err := lc.WhoIs(ctx, remoteAddr)
		if err != nil {
			return gateway.Peer{}, err
		}
		login, err := whoLogin(who)
		if err != nil {
			return gateway.Peer{}, err
		}
		if who.Node == nil || who.Node.StableID == "" {
			return gateway.Peer{}, errors.New("no tailscale node")
		}
		return gateway.Peer{Login: login, NodeID: string(who.Node.StableID)}, nil
	}
}

// hostNodes is the tsnet node's StableID and, when the system tailscaled
// answers, that daemon's. A failed tsnet read is an error, so the console
// refuses; a missing system daemon is not one.
func hostNodes(lc *local.Client) func(context.Context) ([]string, error) {
	var (
		mu    sync.Mutex
		ids   []string
		until time.Time
	)
	return func(ctx context.Context) ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		if time.Now().Before(until) {
			return ids, nil
		}
		st, err := lc.StatusWithoutPeers(ctx)
		if err != nil {
			return nil, err
		}
		if st.Self == nil || st.Self.ID == "" {
			return nil, errors.New("tsnet status has no self node")
		}
		out := []string{string(st.Self.ID)}
		sctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		var sys local.Client
		if sst, err := sys.StatusWithoutPeers(sctx); err == nil && sst.Self != nil {
			if id := strings.TrimSpace(string(sst.Self.ID)); id != "" && id != out[0] {
				out = append(out, id)
			}
		}
		ids, until = out, time.Now().Add(hostNodesTTL)
		return ids, nil
	}
}
