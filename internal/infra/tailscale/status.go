package tailscale

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Status is the subset of `tailscale status --json` that phone setup needs.
type Status struct {
	BackendState      string   `json:"backend_state"`
	AuthURL           string   `json:"auth_url,omitempty"`
	DNSName           string   `json:"dns_name,omitempty"`
	Hostname          string   `json:"hostname,omitempty"`
	IPv4              string   `json:"ipv4,omitempty"`
	IPv6              string   `json:"ipv6,omitempty"`
	Online            bool     `json:"online"`
	MagicDNSEnabled   bool     `json:"magic_dns"`
	MagicDNSSuffix    string   `json:"magic_dns_suffix,omitempty"`
	Tailnet           string   `json:"tailnet,omitempty"`
	MobilePeersOnline []string `json:"mobile_peers_online,omitempty"`
}

// Ready reports whether this host is reachable on the tailnet.
func (s Status) Ready() bool {
	return strings.EqualFold(s.BackendState, "Running") && s.Online && (s.DNSName != "" || s.IPv4 != "")
}

// SSHHost is the address Termius should use (MagicDNS, else IPv4).
func (s Status) SSHHost() string {
	if s.DNSName != "" {
		return s.DNSName
	}
	return s.IPv4
}

type execer func(ctx context.Context, name string, args ...string) ([]byte, error)

type runner func(ctx context.Context, name string, args ...string) error

func defaultExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

func defaultRun(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Client talks to the host `tailscale` CLI. It does not embed Tailscale.
type Client struct {
	LookPath func(string) (string, error)
	Exec     execer
	Run      runner
}

func (c *Client) lookPath(file string) (string, error) {
	if c != nil && c.LookPath != nil {
		return c.LookPath(file)
	}
	return exec.LookPath(file)
}

func (c *Client) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c != nil && c.Exec != nil {
		return c.Exec(ctx, name, args...)
	}
	return defaultExec(ctx, name, args...)
}

// StatusTimeout bounds a status probe.
const StatusTimeout = 5 * time.Second

// Probe runs `tailscale status --json`.
func (c *Client) Probe(ctx context.Context) (Status, error) {
	if _, err := c.lookPath("tailscale"); err != nil {
		return Status{}, fmt.Errorf("tailscale CLI not found on PATH: install from https://tailscale.com/download")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pctx, cancel := context.WithTimeout(ctx, StatusTimeout)
	defer cancel()
	out, err := c.exec(pctx, "tailscale", "status", "--json")
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return Status{}, fmt.Errorf("tailscale status: %s", msg)
	}
	st, perr := ParseStatusJSON(out)
	if perr != nil {
		return Status{}, perr
	}
	return st, nil
}

type statusJSON struct {
	BackendState   string `json:"BackendState"`
	AuthURL        string `json:"AuthURL"`
	MagicDNSSuffix string `json:"MagicDNSSuffix"`
	CurrentTailnet *struct {
		Name            string `json:"Name"`
		MagicDNSEnabled bool   `json:"MagicDNSEnabled"`
		MagicDNSSuffix  string `json:"MagicDNSSuffix"`
	} `json:"CurrentTailnet"`
	Self *struct {
		HostName     string   `json:"HostName"`
		DNSName      string   `json:"DNSName"`
		Online       bool     `json:"Online"`
		TailscaleIPs []string `json:"TailscaleIPs"`
	} `json:"Self"`
	Peer map[string]struct {
		HostName string `json:"HostName"`
		Online   bool   `json:"Online"`
		OS       string `json:"OS"`
	} `json:"Peer"`
}

// ParseStatusJSON decodes `tailscale status --json`.
func ParseStatusJSON(raw []byte) (Status, error) {
	var j statusJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		return Status{}, fmt.Errorf("parse tailscale status: %w", err)
	}
	st := Status{
		BackendState: j.BackendState,
		AuthURL:      strings.TrimSpace(j.AuthURL),
	}
	if j.CurrentTailnet != nil {
		st.Tailnet = j.CurrentTailnet.Name
		st.MagicDNSEnabled = j.CurrentTailnet.MagicDNSEnabled
		st.MagicDNSSuffix = strings.TrimPrefix(j.CurrentTailnet.MagicDNSSuffix, ".")
	}
	if st.MagicDNSSuffix == "" {
		st.MagicDNSSuffix = strings.TrimPrefix(j.MagicDNSSuffix, ".")
	}
	if j.Self != nil {
		st.Hostname = j.Self.HostName
		st.Online = j.Self.Online
		st.DNSName = strings.TrimSuffix(strings.TrimSpace(j.Self.DNSName), ".")
		for _, ip := range j.Self.TailscaleIPs {
			if strings.Contains(ip, ":") {
				if st.IPv6 == "" {
					st.IPv6 = ip
				}
				continue
			}
			if st.IPv4 == "" {
				st.IPv4 = ip
			}
		}
	}
	for _, p := range j.Peer {
		if !p.Online {
			continue
		}
		switch strings.ToLower(p.OS) {
		case "ios", "ipados", "android":
			name := strings.TrimSpace(p.HostName)
			if name == "" {
				name = p.OS
			}
			st.MobilePeersOnline = append(st.MobilePeersOnline, name)
		}
	}
	return st, nil
}

func (c *Client) run(ctx context.Context, name string, args ...string) error {
	if c != nil && c.Run != nil {
		return c.Run(ctx, name, args...)
	}
	return defaultRun(ctx, name, args...)
}

// Up runs `tailscale up` on the host. It may open a browser for login.
func (c *Client) Up(ctx context.Context) error {
	if _, err := c.lookPath("tailscale"); err != nil {
		return fmt.Errorf("tailscale CLI not found on PATH: install from https://tailscale.com/download")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.run(ctx, "tailscale", "up"); err != nil {
		return fmt.Errorf("tailscale up: %w", err)
	}
	return nil
}
