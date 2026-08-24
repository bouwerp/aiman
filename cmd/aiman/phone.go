package main

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/infra/tailscale"
)

var phoneTS = &tailscale.Client{}

func sshUsername() string {
	if u, err := user.Current(); err == nil && u != nil && u.Username != "" {
		return u.Username
	}
	if u := strings.TrimSpace(os.Getenv("USER")); u != "" {
		return u
	}
	return "user"
}

func printPhoneUsage() {
	fmt.Fprint(os.Stderr, `Usage: aiman phone [--up] [--json]

Print Termius SSH details for this host on Tailscale. Run this on the
machine you will SSH into from the iPhone.

  --up    run "tailscale up" if this host is not on the tailnet yet
  --json  machine-readable report

Does not bundle Tailscale. Uses the host "tailscale" CLI. The iPhone
still needs the Tailscale app connected before Termius.
`)
}

func runPhone(args []string) error {
	flags, rest := takeFlags(args)
	if flags["help"] != "" || flags["h"] != "" {
		printPhoneUsage()
		return errUsage
	}
	for _, a := range rest {
		if a == "-h" || a == "--help" {
			printPhoneUsage()
			return errUsage
		}
	}

	ctx := context.Background()
	st, err := phoneTS.Probe(ctx)
	if flags["up"] != "" && (err != nil || !st.Ready()) {
		uctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		if uerr := phoneTS.Up(uctx); uerr != nil {
			return uerr
		}
		st, err = phoneTS.Probe(ctx)
	}
	if err != nil {
		return err
	}

	userName := sshUsername()
	rep := phoneReport(st, userName)
	if flags["json"] != "" {
		if err := writeJSON(rep); err != nil {
			return err
		}
	} else {
		fmt.Print(formatPhoneReport(rep))
	}
	if !st.Ready() {
		return fmt.Errorf("tailscale is %s; connect this host then retry (aiman phone --up)", emptyState(st.BackendState))
	}
	return nil
}

type phoneOut struct {
	Type   string           `json:"type"`
	Ready  bool             `json:"ready"`
	User   string           `json:"user"`
	Host   string           `json:"host"`
	Port   int              `json:"port"`
	SSH    string           `json:"ssh"`
	Status tailscale.Status `json:"tailscale"`
	IPhone []string         `json:"iphone"`
	Next   string           `json:"next,omitempty"`
}

func phoneReport(st tailscale.Status, userName string) phoneOut {
	host := st.SSHHost()
	ssh := ""
	if userName != "" && host != "" {
		ssh = fmt.Sprintf("ssh %s@%s", userName, host)
	}
	out := phoneOut{
		Type:   "phone",
		Ready:  st.Ready(),
		User:   userName,
		Host:   host,
		Port:   22,
		SSH:    ssh,
		Status: st,
		IPhone: iphoneSteps(st, userName),
	}
	if !st.Ready() {
		out.Next = nextTailscaleAction(st)
	}
	return out
}

func emptyState(s string) string {
	if strings.TrimSpace(s) == "" {
		return "not running"
	}
	return s
}

func nextTailscaleAction(st tailscale.Status) string {
	switch {
	case strings.EqualFold(st.BackendState, "NeedsLogin"):
		if st.AuthURL != "" {
			return "tailscale login  (or: aiman phone --up)"
		}
		return "aiman phone --up"
	case !strings.EqualFold(st.BackendState, "Running"):
		return "aiman phone --up"
	case !st.Online:
		return "aiman phone --up"
	default:
		return "aiman phone --up"
	}
}

func iphoneSteps(st tailscale.Status, userName string) []string {
	host := st.SSHHost()
	if host == "" {
		host = "(host after Tailscale is Running)"
	}
	if userName == "" {
		userName = "user"
	}
	steps := []string{
		"Open the Tailscale app and Connect (before Termius).",
		"In iOS Settings → VPN, allow cellular and keep the tunnel on (On Demand).",
		"Termius → New Host: Hostname " + host + "  Port 22  Username " + userName + ".",
		"Connect, then run: aiman",
	}
	if len(st.MobilePeersOnline) > 0 {
		steps = append([]string{"Tailscale already sees: " + strings.Join(st.MobilePeersOnline, ", ")}, steps...)
	}
	return steps
}

func formatPhoneReport(r phoneOut) string {
	var b strings.Builder
	b.WriteString("aiman phone\n")
	fmt.Fprintf(&b, "  Tailscale   %s", emptyState(r.Status.BackendState))
	if r.Status.Online {
		b.WriteString(" (online)")
	}
	b.WriteString("\n")
	if r.Status.Tailnet != "" {
		fmt.Fprintf(&b, "  Tailnet     %s\n", r.Status.Tailnet)
	}
	if r.Host != "" {
		fmt.Fprintf(&b, "  Hostname    %s\n", r.Host)
	}
	if r.Status.IPv4 != "" {
		fmt.Fprintf(&b, "  IPv4        %s\n", r.Status.IPv4)
	}
	fmt.Fprintf(&b, "  SSH user    %s\n", r.User)
	if r.SSH != "" {
		fmt.Fprintf(&b, "  SSH         %s\n", r.SSH)
	}
	if r.Next != "" {
		fmt.Fprintf(&b, "  Next        %s\n", r.Next)
	}
	b.WriteString("\n  iPhone / Termius\n")
	for i, step := range r.IPhone {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, step)
	}
	return b.String()
}
