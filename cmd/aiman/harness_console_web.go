package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"runtime"

	"github.com/bouwerp/aiman/internal/console"
)

type webConsoleDeps struct {
	rpc       console.RPC
	forwarder console.Forwarder
	// listen binds 127.0.0.1 on a port the kernel picks.
	listen func() (net.Listener, error)
	// open shows the URL in the browser; nil or --no-browser only prints it.
	open func(url string) error
	out  io.Writer
	// log takes the console's refusals and failures; never the token.
	log io.Writer
}

// harnessWebConsole serves the web console until ctx ends. The forwards and
// the listener end with it.
func harnessWebConsole(ctx context.Context, a harnessArgs, d webConsoleDeps) error {
	ln, err := d.listen()
	if err != nil {
		return fmt.Errorf("console listener: %w", err)
	}
	s, err := console.New(console.Options{
		RPC: d.rpc, Forwarder: d.forwarder, Session: a.session, Run: a.run, Log: d.log,
	}, ln)
	if err != nil {
		_ = ln.Close()
		return err
	}
	url := s.URL()
	_, _ = fmt.Fprintf(d.out, "Harness console: %s\n", url)
	_, _ = fmt.Fprintln(d.out, "The link carries this console's key; do not share it. ctrl+c stops the console and its forwards.")
	if !a.noBrowser && d.open != nil {
		if err := d.open(url); err != nil {
			_, _ = fmt.Fprintf(d.out, "Open the link yourself: %v\n", err)
		}
	}
	return s.Serve(ctx)
}

// browserOpener is the command that opens a URL on goos, or empty.
func browserOpener(goos string) string {
	switch goos {
	case "darwin":
		return "open"
	case "windows", "plan9", "js", "wasip1":
		return ""
	}
	return "xdg-open"
}

// openBrowser hands the URL to the OS opener without waiting for the browser.
func openBrowser(url string) error {
	name := browserOpener(runtime.GOOS)
	if name == "" {
		return fmt.Errorf("no browser opener on %s", runtime.GOOS)
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("%s is not installed", name)
	}
	cmd := exec.Command(path, url) //nolint:gosec // G204: the opener is a fixed name and url is the console's own
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
