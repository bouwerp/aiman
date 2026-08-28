package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bouwerp/aiman/internal/gateway"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tsnet"
)

var (
	version   = "dev"
	buildTime = ""
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	opts, err := parseFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if opts.version {
		printVersion()
		return nil
	}

	if err := config.EnsureDir(); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{}
	}
	if opts.funnel && !cfg.GatewayFunnelPermitted() {
		return errors.New("funnel is disabled in config (gateway.funnel: false)")
	}

	dir, err := config.GetDir()
	if err != nil {
		return err
	}
	sock := opts.socket
	if sock == "" {
		sock = server.SocketPath(dir)
	}
	tokenPath := filepath.Join(dir, gateway.TokenFileName)
	token, created, err := gateway.LoadOrCreateToken(tokenPath)
	if err != nil {
		return err
	}
	allow := cfg.Gateway.AllowLogins
	if len(opts.allow) > 0 {
		allow = opts.allow
	}

	ts := &tsnet.Server{
		Hostname: opts.hostname,
		Dir:      filepath.Join(dir, "tsnet-gateway"),
		Logf:     log.Printf,
	}
	if k := os.Getenv("TS_AUTHKEY"); k != "" {
		ts.AuthKey = k
	} else if k := os.Getenv("TS_AUTH_KEY"); k != "" {
		ts.AuthKey = k
	}
	defer ts.Close()

	addr := fmt.Sprintf(":%d", opts.port)
	ln, err := listenTS(ts, opts.funnel, addr)
	if err != nil {
		return err
	}
	defer ln.Close()

	auth := gateway.Auth{Token: token, Allow: allow, Funnel: opts.funnel}
	if !opts.funnel {
		who, err := tailnetWhoIs(ts)
		if err != nil {
			return err
		}
		auth.WhoIs = who
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pushStore, err := gateway.OpenPushStore(filepath.Join(dir, gateway.PushFileName))
	if err != nil {
		return err
	}
	notify := gateway.NewNotifier(pushStore, nil)
	go notify.Watch(ctx, sock)
	httpSrv := &http.Server{
		Handler:           (&gateway.Server{Socket: sock, Auth: auth, Push: pushStore}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = httpSrv.Close()
	}()

	logToken(tokenPath, created)
	if opts.funnel {
		log.Printf("WARNING: Funnel is public and anonymous. The bearer token is the only thing between the internet and a shell on this box.")
	}
	logListen(ts, opts.hostname, addr)

	err = httpSrv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type options struct {
	funnel   bool
	hostname string
	port     int
	socket   string
	allow    []string
	version  bool
}

func parseFlags(args []string) (options, error) {
	for _, a := range args {
		if a == "--version" || a == "-v" || a == "version" {
			return options{version: true}, nil
		}
	}
	var opts options
	fs := flag.NewFlagSet("aiman-gateway", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&opts.funnel, "funnel", false, "listen on Funnel (public internet); token is then the only auth")
	fs.StringVar(&opts.hostname, "hostname", "aiman-gateway", "MagicDNS hostname for this node")
	fs.IntVar(&opts.port, "port", 0, "listen port (default 8080 tailnet, 443 Funnel)")
	fs.StringVar(&opts.socket, "socket", "", "aiman serve unix socket (default ~/.aiman/aiman.sock)")
	fs.Func("allow-login", "Tailscale login allowed on the tailnet path (repeatable)", func(v string) error {
		opts.allow = append(opts.allow, v)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if opts.port == 0 {
		opts.port = 8080
		if opts.funnel {
			opts.port = 443
		}
	}
	return opts, nil
}

func tailnetWhoIs(ts *tsnet.Server) (gateway.WhoIs, error) {
	lc, err := ts.LocalClient()
	if err != nil {
		return nil, fmt.Errorf("tailscale local client: %w", err)
	}
	return func(ctx context.Context, remoteAddr string) (string, error) {
		who, err := lc.WhoIs(ctx, remoteAddr)
		if err != nil {
			return "", err
		}
		return whoLogin(who)
	}, nil
}

func whoLogin(who *apitype.WhoIsResponse) (string, error) {
	if who == nil || who.UserProfile == nil {
		return "", errors.New("no tailscale identity")
	}
	if who.UserProfile.LoginName == "" {
		return "", errors.New("no tailscale identity")
	}
	return who.UserProfile.LoginName, nil
}

func logToken(path string, created bool) {
	if created {
		log.Printf("created token file %s (0600); the token is not printed", path)
		return
	}
	log.Printf("token file: %s (token is not printed)", path)
}

func logListen(ts *tsnet.Server, hostname, addr string) {
	if domains := ts.CertDomains(); len(domains) > 0 {
		log.Printf("aiman-gateway listening at https://%s%s", domains[0], addr)
		return
	}
	log.Printf("aiman-gateway listening on %s (%s)", addr, hostname)
}

func printVersion() {
	if buildTime != "" {
		fmt.Printf("aiman-gateway %s (built %s)\n", version, buildTime)
		return
	}
	fmt.Printf("aiman-gateway %s\n", version)
}
