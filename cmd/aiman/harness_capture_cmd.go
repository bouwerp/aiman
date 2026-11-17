package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/bouwerp/aiman/internal/harness/edge"
)

// harnessCapture prints the run's captured HTTP calls, one line each.
func harnessCapture(h harnessIO, a harnessArgs) error {
	params := a.target()
	if a.after > 0 {
		params["after_id"] = a.after
	}
	if a.limit > 0 {
		params["limit"] = a.limit
	}
	raw, err := harnessCallOut(h, a, "harness.capture", params)
	if err != nil {
		return err
	}
	if a.json {
		return printHarnessJSON(h.out, raw)
	}
	var res struct {
		Run     string      `json:"run"`
		Network string      `json:"network"`
		Flows   []edge.Flow `json:"flows"`
		Next    int64       `json:"next"`
		More    bool        `json:"more"`
		Total   int64       `json:"total"`
		Dropped int64       `json:"dropped"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return err
	}
	writeCapture(h.out, res.Run, res.Network, res.Flows, res.Total, res.Dropped)
	if res.More {
		_, _ = fmt.Fprintf(h.out, "more: aiman harness capture --run %s --after %d\n", res.Run, res.Next)
	}
	return nil
}

func writeCapture(w io.Writer, run, network string, flows []edge.Flow, total, dropped int64) {
	_, _ = fmt.Fprintf(w, "run %s, %s, %d calls", run, network, total)
	if dropped > 0 {
		_, _ = fmt.Fprintf(w, " (%d oldest dropped)", dropped)
	}
	_, _ = fmt.Fprintln(w)
	for _, f := range flows {
		status := "-"
		if f.Status > 0 {
			status = strconv.Itoa(f.Status)
		}
		_, _ = fmt.Fprintf(w, "%5d %s %-7s %s %-8s %s %.0fms\n",
			f.ID, f.Start.Local().Format("15:04:05"), f.Method, status, f.Outcome, f.URL, f.Duration)
		if f.Error != "" {
			_, _ = fmt.Fprintf(w, "      %s\n", f.Error)
		}
	}
}
