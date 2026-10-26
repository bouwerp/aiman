package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usage"
)

const usageTableHeader = "KEY  INPUT  OUTPUT  CACHE_READ  CACHE_WRITE  ~COST  CACHE"

func runUsage(args []string, out io.Writer) error {
	flags, rest := takeFlags(args)
	if len(rest) != 0 {
		fmt.Fprintf(os.Stderr, "Usage: aiman usage [--session ID] [--group NAME] [--issue KEY] [--day YYYY-MM-DD] [--by session|issue|day|model] [--days N] [--tz-offset-minutes N] [--json]\n")
		return errUsage
	}
	params, err := usageParams(flags)
	if err != nil {
		writeCLIError(server.CodeInvalidParams, err.Error())
		return err
	}
	return callUsage(params, flags["json"] == "1", out)
}

// usageParams maps CLI flags onto usage.get. An empty session is omitted so
// the server rolls up every session. The zone defaults to the caller's offset.
func usageParams(flags map[string]string) (map[string]any, error) {
	params := map[string]any{}
	for _, key := range []string{"session", "group", "issue", "day", "by"} {
		if v := strings.TrimSpace(flags[key]); v != "" {
			params[key] = v
		}
	}
	if days := flags["days"]; days != "" {
		n, err := strconv.Atoi(days)
		if err != nil {
			return nil, fmt.Errorf("days must be an integer")
		}
		params["days"] = n
	}
	offset, err := tzOffsetMinutes(flags["tz-offset-minutes"])
	if err != nil {
		return nil, err
	}
	params["tz_offset_minutes"] = offset
	return params, nil
}

func tzOffsetMinutes(raw string) (int, error) {
	if raw == "" {
		_, sec := time.Now().Zone()
		return sec / 60, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("tz-offset-minutes must be an integer")
	}
	return n, nil
}

func callUsage(params map[string]any, asJSON bool, out io.Writer) error {
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	resp, err := server.Call(sock, "usage.get", params)
	if err != nil {
		code := server.CodeServerNotRunning
		if !errors.Is(err, server.ErrServerNotRunning) {
			code = server.CodeInvalidParams
		}
		writeCLIError(code, err.Error())
		return err
	}
	if resp.Error != nil {
		writeCLIError(resp.Error.Code, resp.Error.Message)
		return errors.New(resp.Error.Message)
	}
	if asJSON {
		return writeJSON(resp.Result)
	}
	rep, err := usageReportFromResult(resp.Result)
	if err != nil {
		return err
	}
	return formatUsageReport(out, rep)
}

func usageReportFromResult(result any) (usage.Report, error) {
	m, ok := result.(map[string]any)
	if !ok {
		return usage.Report{}, errors.New("usage result is not an object")
	}
	rawUsage, ok := m["usage"]
	if !ok || rawUsage == nil {
		return usage.Report{}, errors.New("usage result has no usage")
	}
	raw, err := json.Marshal(rawUsage)
	if err != nil {
		return usage.Report{}, err
	}
	var rep usage.Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		return usage.Report{}, fmt.Errorf("usage report: %w", err)
	}
	return rep, nil
}

func formatUsageReport(w io.Writer, rep usage.Report) error {
	if _, err := io.WriteString(w, usageTableHeader+"\n"); err != nil {
		return err
	}
	rows := rep.Rows
	if len(rows) == 0 {
		rows = []usage.Row{{Key: "(total)", Summary: rep.Total}}
	}
	for _, row := range rows {
		if _, err := io.WriteString(w, formatUsageRow(row)+"\n"); err != nil {
			return err
		}
	}
	if len(rep.Unpriced) == 0 {
		return nil
	}
	_, err := io.WriteString(w, usageUnpricedFooter(rep.Unpriced))
	return err
}

func usageUnpricedFooter(models []string) string {
	return "unpriced: " + strings.Join(models, ", ") + " (tokens only; set usage.prices in ~/.aiman/config.yaml to see ~$)\n"
}

func formatUsageRow(row usage.Row) string {
	tokens := row.Summary.Tokens
	cols := []string{
		row.Key,
		usage.FormatTokens(tokens.Input),
		usage.FormatTokens(tokens.Output),
		usage.FormatTokens(tokens.CacheRead),
		usage.FormatTokens(tokens.CacheCreation()),
		usageCostCell(row.Summary),
		usageCacheCell(row.Summary),
	}
	return strings.Join(cols, "  ")
}

func usageCostCell(s usage.Summary) string {
	if cost := usage.FormatUsageCost(s); cost != "" {
		return cost
	}
	return "-"
}

func usageCacheCell(s usage.Summary) string {
	if s.CacheHitRatio == nil {
		return "-"
	}
	pct := int(math.Round(*s.CacheHitRatio * 100))
	return strconv.Itoa(pct) + "%"
}
