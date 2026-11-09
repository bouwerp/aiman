package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/bouwerp/aiman/internal/server"
)

// runHost is `aiman host stats|version|update`.
func runHost(args []string) error {
	if len(args) > 0 && (args[0] == "version" || args[0] == "update") {
		return runHostUpdateCmd(args, os.Stdout)
	}
	asJSON, ok := hostStatsArgs(args)
	if !ok {
		fmt.Fprint(os.Stderr, hostUsage)
		return errUsage
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	if asJSON {
		return callAndPrint(sock, "host.stats", nil)
	}
	resp, err := server.Call(sock, "host.stats", nil)
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	if resp.Error != nil {
		writeCLIResponseError(resp.Error)
		return errors.New(resp.Error.Message)
	}
	return printHostStats(os.Stdout, resp.Result)
}

// hostStatsArgs accepts `stats` and `stats --json`.
func hostStatsArgs(args []string) (asJSON, ok bool) {
	if len(args) == 0 || args[0] != "stats" {
		return false, false
	}
	for _, a := range args[1:] {
		if a != "--json" {
			return false, false
		}
		asJSON = true
	}
	return asJSON, true
}

// hostStatsText is the part of host.stats the text form prints.
type hostStatsText struct {
	Host       string          `json:"host"`
	Load       [3]float64      `json:"load"`
	CPUCount   int             `json:"cpu_count"`
	CPUPercent float64         `json:"cpu_percent"`
	RPC        server.RPCStats `json:"rpc"`
}

// printHostStats writes the host line and the rpc block.
func printHostStats(w io.Writer, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	var st hostStatsText
	if err := json.Unmarshal(raw, &st); err != nil {
		return err
	}
	fmt.Fprintf(w, "host %s · load %.2f %.2f %.2f · cpu %.1f%% of %d\n",
		st.Host, st.Load[0], st.Load[1], st.Load[2], st.CPUPercent, st.CPUCount)
	r := st.RPC
	fmt.Fprintf(w, "rpc (last %ds)\n", r.WindowSeconds)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range []struct {
		name string
		v    int
	}{
		{"total/min", r.Total}, {"inflight", r.Inflight}, {"waiting", r.Waiting},
		{"rejected", r.Rejected}, {"limited", r.Limited}, {"sweep_skipped", r.SweepSkipped},
	} {
		fmt.Fprintf(tw, "  %s\t%d\n", row.name, row.v)
	}
	printRPCCounts(tw, "methods", r.ByMethod)
	printRPCCounts(tw, "callers", r.ByCaller)
	return tw.Flush()
}

func printRPCCounts(w io.Writer, title string, counts []server.RPCCount) {
	if len(counts) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s\n", title)
	for _, c := range counts {
		fmt.Fprintf(w, "    %s\t%d\tavg %s\n", c.Name, c.Count, formatUS(c.AvgUS))
	}
}

// formatUS is a duration in microseconds, in the largest unit that keeps it
// readable.
func formatUS(us int64) string {
	switch {
	case us >= 1_000_000:
		return fmt.Sprintf("%.1fs", float64(us)/1e6)
	case us >= 1000:
		return fmt.Sprintf("%.1fms", float64(us)/1e3)
	default:
		return fmt.Sprintf("%dµs", us)
	}
}
