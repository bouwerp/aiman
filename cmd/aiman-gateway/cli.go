package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bouwerp/aiman/internal/gateway"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// osHostname is os.Hostname, swapped in tests.
var osHostname = os.Hostname

// hostName is the OS hostname, the host the app shows and deep links name.
func hostName() string {
	h, err := osHostname()
	if err != nil {
		return ""
	}
	return h
}

// isCLI reports whether args name a host-side subcommand rather than the
// service.
func isCLI(args []string) bool {
	return len(args) > 0 && (args[0] == "pair" || args[0] == "devices")
}

// runCLI runs `pair` or `devices`, writing to out.
func runCLI(args []string, out io.Writer) error {
	switch args[0] {
	case "pair":
		return runPair(args[1:], out)
	case "devices":
		return runDevices(args[1:], out)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

type pairOutput struct {
	Target    string    `json:"target"`
	Host      string    `json:"host"`
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
	URI       string    `json:"uri"`
}

// runPair mints a pairing code. It takes the service's --hostname, --port
// and --funnel so the target matches a service started with the same flags.
func runPair(args []string, out io.Writer) error {
	var opts options
	var name string
	var asJSON bool
	fs := flag.NewFlagSet("aiman-gateway pair", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	addServiceFlags(fs, &opts)
	fs.StringVar(&name, "name", "", "device name used when the phone does not send one")
	fs.BoolVar(&asJSON, "json", false, "print {target,host,code,expires_at,uri}")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resolveServiceDefaults(&opts)
	if opts.funnel {
		return errors.New("pairing needs a tailnet identity and is refused on Funnel")
	}
	dir, err := config.GetDir()
	if err != nil {
		return err
	}
	code, expires, err := gateway.NewPairCodes(filepath.Join(dir, gateway.PairCodesFileName)).Mint(name)
	if err != nil {
		return err
	}
	res := pairOutput{
		Target:    fmt.Sprintf("%s:%d", opts.hostname, opts.port),
		Host:      hostName(),
		Code:      code,
		ExpiresAt: expires.UTC(),
	}
	q := url.Values{}
	q.Set("target", res.Target)
	q.Set("host", res.Host)
	q.Set("code", res.Code)
	res.URI = "aimanphone://pair?" + q.Encode()
	if asJSON {
		return json.NewEncoder(out).Encode(res)
	}
	_, err = fmt.Fprintf(out, "Code:    %s\nExpires: %s\nTarget:  %s\nHost:    %s\nURI:     %s\n",
		res.Code, res.ExpiresAt.Local().Format(time.Kitchen), res.Target, res.Host, res.URI)
	return err
}

// runDevices is `devices [list [--json] | revoke <id> | allow-destructive <id> on|off]`.
func runDevices(args []string, out io.Writer) error {
	dir, err := config.GetDir()
	if err != nil {
		return err
	}
	store := gateway.NewDeviceStore(filepath.Join(dir, gateway.DevicesFileName))
	if len(args) == 0 {
		return listDevices(store, nil, out)
	}
	switch args[0] {
	case "list":
		return listDevices(store, args[1:], out)
	case "revoke":
		if len(args) != 2 {
			return errors.New("usage: aiman-gateway devices revoke <id>")
		}
		if err := store.Revoke(args[1]); err != nil {
			return err
		}
		_, err := fmt.Fprintf(out, "revoked %s\n", args[1])
		return err
	case "allow-destructive":
		return allowDestructive(store, args[1:], out)
	default:
		return fmt.Errorf("unknown devices command %q (list, revoke, allow-destructive)", args[0])
	}
}

func listDevices(store *gateway.DeviceStore, args []string, out io.Writer) error {
	var asJSON bool
	fs := flag.NewFlagSet("aiman-gateway devices list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&asJSON, "json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	devs, err := store.List()
	if err != nil {
		return err
	}
	infos := make([]gateway.DeviceInfo, 0, len(devs))
	for _, d := range devs {
		infos = append(infos, d.Info())
	}
	if asJSON {
		return json.NewEncoder(out).Encode(map[string]any{"devices": infos})
	}
	if len(infos) == 0 {
		_, err := fmt.Fprintln(out, "no paired devices")
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tNAME\tLOGIN\tCREATED\tLAST SEEN\tDESTRUCTIVE")
	for _, d := range infos {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%v\n", d.ID, d.Name, dash(d.Login),
			d.CreatedAt.Local().Format(time.DateTime), formatSeen(d.LastSeen), d.AllowDestructive)
	}
	return tw.Flush()
}

func allowDestructive(store *gateway.DeviceStore, args []string, out io.Writer) error {
	if len(args) != 2 {
		return errors.New("usage: aiman-gateway devices allow-destructive <id> on|off")
	}
	var on bool
	switch strings.ToLower(args[1]) {
	case "on":
		on = true
	case "off":
	default:
		return fmt.Errorf("allow-destructive takes on or off, not %q", args[1])
	}
	if err := store.SetAllowDestructive(args[0], on); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "%s: allow_destructive %s\n", args[0], strings.ToLower(args[1]))
	return err
}

func formatSeen(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format(time.DateTime)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
