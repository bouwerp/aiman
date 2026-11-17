package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/bouwerp/aiman/internal/harness"
)

// harnessDB is `aiman harness db <service> tables|rows <table>|query <sql>`.
// A read goes to serve's database browser; --write runs the statement as the
// database's owner and is the operator's.
func harnessDB(h harnessIO, a harnessArgs) error {
	service, op := a.pos[0], a.pos[1]
	params := a.target()
	params["service"], params["op"] = service, op
	switch op {
	case harness.DBOpTables:
		if len(a.pos) != 2 {
			return harnessUsage(h.errOut)
		}
	case harness.DBOpRows:
		if len(a.pos) != 3 {
			return harnessUsage(h.errOut)
		}
		params["table"] = a.pos[2]
	case harness.DBOpQuery:
		if len(a.pos) != 3 {
			return harnessUsage(h.errOut)
		}
		params["sql"] = a.pos[2]
	default:
		return harnessUsage(h.errOut)
	}
	if a.write {
		if op != harness.DBOpQuery {
			_, _ = fmt.Fprintln(h.errOut, "--write takes query <sql>")
			return harnessUsage(h.errOut)
		}
		params["write"] = true
	}
	if a.limit > 0 {
		params["limit"] = a.limit
	}
	raw, err := harnessCallOut(h, a, "harness.db", params)
	if err != nil {
		return err
	}
	if a.json {
		return printHarnessJSON(h.out, raw)
	}
	var res harness.DBResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return err
	}
	printDBResult(h, res)
	return nil
}

func printDBResult(h harnessIO, res harness.DBResult) {
	switch {
	case res.Tables != nil:
		for _, t := range res.Tables {
			_, _ = fmt.Fprintln(h.out, t)
		}
	case len(res.Columns) > 0:
		tw := tabwriter.NewWriter(h.out, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, strings.Join(res.Columns, "\t"))
		for _, row := range res.Rows {
			cells := make([]string, len(row))
			for i, c := range row {
				cells[i] = "NULL"
				if c != nil {
					cells[i] = strings.NewReplacer("\t", " ", "\n", " ").Replace(*c)
				}
			}
			_, _ = fmt.Fprintln(tw, strings.Join(cells, "\t"))
		}
		_ = tw.Flush()
	}
	for _, l := range res.Output {
		_, _ = fmt.Fprintln(h.out, l)
	}
	var notes []string
	if len(res.Columns) > 0 {
		notes = append(notes, fmt.Sprintf("%d rows", len(res.Rows)))
	}
	if res.Truncated {
		notes = append(notes, fmt.Sprintf("truncated at %d", res.Limit))
	}
	if len(res.RedactedColumns) > 0 {
		notes = append(notes, "redacted columns: "+strings.Join(res.RedactedColumns, ", "))
	}
	if res.Redacted > 0 {
		notes = append(notes, fmt.Sprintf("%d values redacted", res.Redacted))
	}
	notes = append(notes, "as "+res.Role)
	_, _ = fmt.Fprintf(h.errOut, "(%s)\n", strings.Join(notes, "; "))
}
