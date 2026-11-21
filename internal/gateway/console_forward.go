package gateway

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
)

// App-port forwards (docs/specs/harness-share-design.md, "tsnet app
// forwards"): while a share is live and its run ready, the gateway listens on
// the tailnet at the host port of each app port and proxies TCP to the edge
// at 127.0.0.1:<host port>. Only app ports are forwarded, never a relay (db,
// evm, aws, dap) and never the gateway's own ports. Each connection passes
// the peer check and needs a live visit token for the share held by the same
// login; anything else is closed without a dial.

// gatewayPorts are never forwarded: the gateway's API and console.
var gatewayPorts = map[int]bool{8080: true, 8090: true}

// forwardDialTimeout bounds the dial to the edge.
const forwardDialTimeout = 5 * time.Second

// appForward is one listener for one app port of a share.
type appForward struct {
	port int
	ln   net.Listener
	once sync.Once
}

func (f *appForward) close() {
	f.once.Do(func() { _ = f.ln.Close() })
}

// forwardPorts are the run's app ports that may be forwarded.
func (c *ConsoleShares) forwardPorts(ports []harness.RunPort) map[int]bool {
	out := map[int]bool{}
	for _, p := range ports {
		if p.Service != "app" || p.Name == harness.RelayDAP || p.HostPort <= 0 || p.HostPort > 65535 {
			continue
		}
		if gatewayPorts[p.HostPort] || p.HostPort == c.o.Port {
			continue
		}
		out[p.HostPort] = true
	}
	return out
}

// syncForwards opens the forwards for ports and closes the rest; nil ports
// closes every forward of the share.
func (c *ConsoleShares) syncForwards(sh *share, ports []harness.RunPort) {
	want := c.forwardPorts(ports)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	for port, f := range sh.fwds {
		if !want[port] {
			f.close()
			delete(sh.fwds, port)
		}
	}
	if sh.ended || c.o.Listen == nil {
		return
	}
	for port := range want {
		if sh.fwds[port] != nil {
			continue
		}
		ln, err := c.o.Listen("tcp", ":"+strconv.Itoa(port))
		if err != nil {
			c.logf("forward for port %d: %v", port, err)
			continue
		}
		f := &appForward{port: port, ln: ln}
		sh.fwds[port] = f
		go c.acceptForward(sh, f)
	}
}

// acceptForward admits visitors on f until its listener closes.
func (c *ConsoleShares) acceptForward(sh *share, f *appForward) {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go c.serveForward(sh, f, conn)
	}
}

func (c *ConsoleShares) serveForward(sh *share, f *appForward, conn net.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), forwardDialTimeout)
	defer cancel()
	p, ok := c.peer(ctx, conn.RemoteAddr().String())
	if !ok || !c.holds(sh, p.Login) {
		_ = conn.Close()
		return
	}
	in, ok := sh.track(conn)
	if !ok {
		_ = conn.Close()
		return
	}
	defer func() { _ = in.Close() }()
	out, err := c.o.Dial(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(f.port))
	if err != nil {
		c.logf("forward for port %d: dial failed", f.port)
		return
	}
	outT, ok := sh.track(out)
	if !ok {
		_ = out.Close()
		return
	}
	defer func() { _ = outT.Close() }()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(outT, in); done <- struct{}{} }()
	go func() { _, _ = io.Copy(in, outT); done <- struct{}{} }()
	<-done
}
