package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/bouwerp/aiman/internal/infra/config"
)

const remotesUsage = "Usage: aiman remotes [--json]\n" +
	"Lists the remotes in ~/.aiman/config.yaml. --remote takes a NAME, or a user@host one remote has.\n"

// remoteRow is one remote as `aiman remotes --json` prints it.
type remoteRow struct {
	Name   string `json:"name"`
	User   string `json:"user"`
	Host   string `json:"host"`
	Root   string `json:"root"`
	Target string `json:"target"`
	Active bool   `json:"active"`
}

// runRemotes lists the laptop config's remotes. It reads only the config
// file and never calls serve.
func runRemotes(args []string, out io.Writer) error {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		case "-h", "--help":
			_, _ = io.WriteString(out, remotesUsage)
			return nil
		default:
			fmt.Fprintf(os.Stderr, "aiman remotes: unknown argument %s\n%s", a, remotesUsage)
			return errUsage
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("read ~/.aiman/config.yaml: %w", err)
	}
	return writeRemotes(out, cfg, asJSON)
}

func writeRemotes(out io.Writer, cfg *config.Config, asJSON bool) error {
	rows := make([]remoteRow, 0, len(cfg.Remotes))
	for _, r := range cfg.Remotes {
		rows = append(rows, remoteRow{Name: r.Name, User: r.User, Host: r.Host, Root: r.Root, Target: r.Target(), Active: r.IsActive(cfg.ActiveRemote)})
	}
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"remotes": rows, "active_remote": cfg.ActiveRemote})
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(out, "no remotes in ~/.aiman/config.yaml (add one in the TUI: aiman init)")
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tUSER\tHOST\tROOT\tACTIVE")
	for _, r := range rows {
		active := ""
		if r.Active {
			active = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, dash(r.User), dash(r.Host), dash(r.Root), active)
	}
	return tw.Flush()
}
