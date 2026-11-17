package images

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Proxy defaults.
const (
	// ProxyPort is where the review proxy listens inside its network.
	ProxyPort = 3128
	// proxyMaxConns caps concurrent tunnels.
	proxyMaxConns = 64
	// proxyHeaderTimeout bounds reading a CONNECT request.
	proxyHeaderTimeout = 10 * time.Second
	// proxyDialTimeout bounds the upstream dial.
	proxyDialTimeout = 15 * time.Second
	// ProxyReadyLine is what the proxy prints once it listens.
	ProxyReadyLine = "aiman review proxy listening on "
)

// Proxy is the review container's only way out: an HTTP CONNECT proxy that
// tunnels to the exact host:port pairs in Allow and refuses everything else,
// plain HTTP requests included. It never reads or alters the TLS inside a
// tunnel.
type Proxy struct {
	// Allow lists host:port pairs, compared case-insensitively.
	Allow []string
	// Dial reaches an upstream; nil is a net.Dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Logf logs refusals (host only); nil is log.Printf.
	Logf func(format string, args ...any)
}

func (p *Proxy) allowed(hostport string) bool {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil || host == "" || port == "" {
		return false
	}
	want := strings.ToLower(net.JoinHostPort(host, port))
	for _, a := range p.Allow {
		if strings.ToLower(a) == want {
			return true
		}
	}
	return false
}

func (p *Proxy) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Serve accepts connections on l until ctx ends or l fails.
func (p *Proxy) Serve(ctx context.Context, l net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	sem := make(chan struct{}, proxyMaxConns)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case sem <- struct{}{}:
		default:
			_ = c.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			p.handle(ctx, c)
		}()
	}
}

func (p *Proxy) handle(ctx context.Context, c net.Conn) {
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(proxyHeaderTimeout))
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	target := req.Host
	if req.Method != http.MethodConnect || !p.allowed(target) {
		p.logf("review proxy: refused %s %s", req.Method, printable(truncate(target, 256), false))
		_, _ = io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	dial := p.Dial
	if dial == nil {
		d := &net.Dialer{Timeout: proxyDialTimeout}
		dial = d.DialContext
	}
	dctx, cancel := context.WithTimeout(ctx, proxyDialTimeout)
	up, err := dial(dctx, "tcp", strings.ToLower(target))
	cancel()
	if err != nil {
		p.logf("review proxy: dial %s: %v", target, err)
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	defer func() { _ = up.Close() }()
	_ = c.SetReadDeadline(time.Time{})
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		// Bytes the client sent after the request line are in br.
		_, _ = io.Copy(up, br)
		closeWrite(up)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(c, up)
		closeWrite(c)
		done <- struct{}{}
	}()
	select {
	case <-done:
		<-done
	case <-ctx.Done():
	}
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

// ProxyMain is `aiman harness review-proxy --listen ADDR --allow HOST:PORT...`,
// which runs inside the review proxy container. --listen auto:PORT binds the
// first non-loopback IPv4 address the container has when it starts: the
// review network's, before serve connects the container to the bridge, so
// nothing on the bridge can use the proxy.
func ProxyMain(args []string) error {
	fs := flag.NewFlagSet("harness review-proxy", flag.ContinueOnError)
	listen := fs.String("listen", fmt.Sprintf("auto:%d", ProxyPort), "address to listen on, or auto:PORT")
	var allow multiFlag
	fs.Var(&allow, "allow", "host:port the proxy may reach (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(allow) == 0 {
		return errors.New("at least one --allow is required")
	}
	addr, err := listenAddr(*listen)
	if err != nil {
		return err
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	_, _ = fmt.Fprintln(os.Stderr, ProxyReadyLine+l.Addr().String())
	return (&Proxy{Allow: allow}).Serve(ctx, l)
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func listenAddr(spec string) (string, error) {
	port, ok := strings.CutPrefix(spec, "auto:")
	if !ok {
		return spec, nil
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
			return net.JoinHostPort(ipn.IP.String(), port), nil
		}
	}
	return "", errors.New("no non-loopback IPv4 address to listen on")
}
