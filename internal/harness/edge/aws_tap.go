package edge

import (
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// AWSTapPort is the port the tap listens on at the edge's inner address,
// which the aws service's name resolves to on the inner network.
const AWSTapPort = 4566

// awsTapRefused are emulator admin paths the tap answers 403: they change
// the emulator's configuration or reach services a run does not load.
var awsTapRefused = []string{
	"/_ministack/config", "/_ministack/cfn-", "/_ministack/lambda-", "/_ministack/eks-auth",
	"/_ministack/rds/", "/_ministack/transfer/",
}

// AWSTap forwards the app's AWS calls to the emulator unchanged: host,
// path, query, headers and body stay as sent, so SigV4 stays valid, and
// bodies stream both ways.
type AWSTap struct {
	rp  *httputil.ReverseProxy
	srv *http.Server
}

// NewAWSTap returns a tap to target (host:port of the emulator).
func NewAWSTap(target string) (*AWSTap, error) {
	if _, _, err := net.SplitHostPort(target); err != nil {
		return nil, errors.New("aws tap target must be host:port")
	}
	u := &url.URL{Scheme: "http", Host: target}
	t := &AWSTap{}
	t.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.Out.Host = pr.In.Host
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
	}
	t.srv = &http.Server{Handler: t, ReadHeaderTimeout: handshakeTimeout, IdleTimeout: idleConnTimeout, ErrorLog: log.New(io.Discard, "", 0)}
	return t, nil
}

// ServeHTTP refuses the admin paths and forwards everything else.
func (t *AWSTap) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, p := range awsTapRefused {
		if strings.HasPrefix(r.URL.Path, p) {
			http.Error(w, "aiman edge: "+r.URL.Path+" is not reachable in a harness run", http.StatusForbidden)
			return
		}
	}
	t.rp.ServeHTTP(w, r)
}

// Serve answers on ln until Close.
func (t *AWSTap) Serve(ln net.Listener) error {
	err := t.srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the tap.
func (t *AWSTap) Close() error { return t.srv.Close() }
