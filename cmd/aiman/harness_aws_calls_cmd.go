package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/bouwerp/aiman/internal/harness/edge"
)

func init() {
	harnessAWSCommands["calls"] = func(h harnessIO, args []string) error {
		return runHarnessSub(h, "aws calls", harnessSub{0, 0, true, harnessAWSCalls}, args)
	}
}

// harnessAWSCalls prints the run's AWS API calls through the tap, one line
// each: `aiman harness aws calls [--after N] [--limit N] [--run ID] [--json]`.
func harnessAWSCalls(h harnessIO, a harnessArgs) error {
	if len(a.pos) != 0 {
		return harnessUsage(h.errOut)
	}
	params := a.target()
	if a.after > 0 {
		params["after_id"] = a.after
	}
	if a.limit > 0 {
		params["limit"] = a.limit
	}
	raw, err := harnessCallOut(h, a, "harness.aws_calls", params)
	if err != nil {
		return err
	}
	if a.json {
		return printHarnessJSON(h.out, raw)
	}
	var res struct {
		Run     string         `json:"run"`
		Service string         `json:"service"`
		Calls   []edge.AWSCall `json:"calls"`
		Next    int64          `json:"next"`
		More    bool           `json:"more"`
		Total   int64          `json:"total"`
		Dropped int64          `json:"dropped"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return err
	}
	writeAWSCalls(h.out, res.Run, res.Service, res.Calls, res.Total, res.Dropped)
	if res.More {
		_, _ = fmt.Fprintf(h.out, "more: aiman harness aws calls --run %s --after %d\n", res.Run, res.Next)
	}
	return nil
}

func writeAWSCalls(w io.Writer, run, service string, calls []edge.AWSCall, total, dropped int64) {
	_, _ = fmt.Fprintf(w, "run %s, aws service %s, %d calls", run, service, total)
	if dropped > 0 {
		_, _ = fmt.Fprintf(w, " (%d oldest dropped)", dropped)
	}
	_, _ = fmt.Fprintln(w)
	for _, c := range calls {
		status := "-"
		if c.Status > 0 {
			status = strconv.Itoa(c.Status)
		}
		var flags []string
		if c.ErrorCode != "" {
			flags = append(flags, c.ErrorCode)
		}
		if c.Presigned {
			flags = append(flags, "presigned")
		}
		if c.ForeignKey {
			flags = append(flags, "key not test")
		}
		if c.Pending {
			flags = append(flags, "pending")
		}
		_, _ = fmt.Fprintf(w, "%5d %s %-8s %-15s %-28s %s %s %.0fms %s\n",
			c.ID, c.Start.Local().Format("15:04:05"), c.Client, c.Service, c.Operation, status,
			awsResourceText(c.Resource), c.Duration, strings.Join(flags, " "))
		if c.Error != "" {
			_, _ = fmt.Fprintf(w, "      %s\n", c.Error)
		}
	}
}

// awsResourceText is a call's resource in one word.
func awsResourceText(r edge.AWSResource) string {
	switch {
	case r.Bucket != "" && r.Key != "":
		return r.Bucket + "/" + r.Key
	case r.Bucket != "":
		return r.Bucket
	case r.Table != "" && r.Index != "":
		return r.Table + "#" + r.Index
	case r.Table != "":
		return r.Table
	case r.Queue != "":
		return r.Queue
	case r.Topic != "":
		return r.Topic
	}
	return "-"
}
