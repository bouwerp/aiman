package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/bouwerp/aiman/internal/cgroup"
	"github.com/bouwerp/aiman/internal/server"
)

const resourcesUsage = "Usage: aiman resources [--session NAME] [--json]\n"

// runLimitsOrResources routes `aiman limits` (provider limits) and `aiman
// resources` (session memory and CPU), the two read-only usage reports.
func runLimitsOrResources(args []string, in io.Reader, out io.Writer) error {
	if args[0] == "resources" {
		return runResources(args[1:], out)
	}
	return runLimits(args[1:], in, out)
}

// runResources is `aiman resources`: each session's memory, CPU and
// pressure against its limits, with the slice and host totals.
func runResources(args []string, out io.Writer) error {
	flags, rest := takeFlags(args)
	if len(rest) != 0 {
		fmt.Fprint(os.Stderr, resourcesUsage)
		return errUsage
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	params := map[string]any{}
	if v := flags["session"]; v != "" {
		params["session"] = v
	}
	if flags["json"] != "" {
		return callAndPrint(sock, "resources.get", params)
	}
	resp, err := server.Call(sock, "resources.get", params)
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	if resp.Error != nil {
		writeCLIResponseError(resp.Error)
		return errors.New(resp.Error.Message)
	}
	return printResources(out, resp.Result)
}

func printResources(out io.Writer, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	var body struct {
		Resources server.ResourcesReport `json:"resources"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return err
	}
	r := body.Resources
	if !r.Supported {
		fmt.Fprintln(out, "unsupported: this host has no user systemd, so sessions run without their own limits")
		return nil
	}
	fmt.Fprintf(out, "host pressure: memory %.0f%%  cpu %.0f%%  io %.0f%%\n", r.Host.Memory, r.Host.CPU, r.Host.IO)
	if u := r.UserSlice; u != nil {
		fmt.Fprintf(out, "user slice:    %s\n", memoryOfLimit(u.MemoryCurrent, u.MemoryHigh))
	}
	if u := r.SessionsSlice; u != nil {
		fmt.Fprintf(out, "all sessions:  %s\n", memoryOfLimit(u.MemoryCurrent, u.MemoryHigh))
	}
	if r.Missing != "" {
		fmt.Fprintf(out, "session %s has no scope: it runs without limits until its terminal is restarted or the operator runs aiman session adopt-scope\n", r.Missing)
		return nil
	}
	if len(r.Sessions) == 0 {
		fmt.Fprintln(out, "no session scopes")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SESSION\tMEMORY\tMAX\tANON\tSHMEM\tPRESSURE\tCPU\tPIDS\tOOM")
	for _, s := range r.Sessions {
		name := s.Name
		if name == "" {
			name = s.ID
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%.0f%%\t%.0f%%\t%d\t%d\n", name,
			memoryOfLimit(s.MemoryCurrent, s.MemoryHigh), cgroup.FormatSize(s.MemoryMax),
			cgroup.FormatSize(s.Anon), cgroup.FormatSize(s.Shmem), s.MemoryPressure, s.CPUPercent, s.Pids, s.OOMKills)
	}
	return w.Flush()
}

// memoryOfLimit renders "5.2G/6G (87%)", or the bare size with no limit.
func memoryOfLimit(cur, high int64) string {
	if high <= 0 {
		return cgroup.FormatSize(cur)
	}
	return fmt.Sprintf("%s/%s (%d%%)", cgroup.FormatSize(cur), cgroup.FormatSize(high), cur*100/high)
}
