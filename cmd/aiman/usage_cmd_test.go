package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usage"
)

func TestUsageParams(t *testing.T) {
	_, offSec := time.Now().Zone()
	wantOff := offSec / 60
	got, err := usageParams(map[string]string{
		"session": "s1",
		"by":      "issue",
		"days":    "30",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["session"] != "s1" || got["by"] != "issue" || got["days"] != 30 || got["tz_offset_minutes"] != wantOff {
		t.Fatalf("params = %#v, want session s1, by issue, days 30, tz %d", got, wantOff)
	}
	if _, err := usageParams(map[string]string{"days": "x"}); err == nil {
		t.Fatal(`days "x" should error`)
	}
	all, err := usageParams(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := all["session"]; ok {
		t.Fatalf("empty session should be omitted, got %#v", all)
	}
	if all["tz_offset_minutes"] != wantOff || all["days"] != nil {
		t.Fatalf("unfiltered params = %#v", all)
	}
}

func TestUsageReportFromResultReadsUsageObject(t *testing.T) {
	rep, err := usageReportFromResult(map[string]any{
		"type":    "usage",
		"session": "s1",
		"usage": map[string]any{
			"known": true,
			"total": map[string]any{"cost_usd": 1.5, "priced": true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Known || !rep.Total.Priced || rep.Total.CostUSD != 1.5 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestFormatUsageReportTable(t *testing.T) {
	ratio := 0.81
	rep := usage.Report{
		Rows: []usage.Row{
			{
				Key: "PROJ-1",
				Summary: usage.Summary{
					Tokens:        domain.TokenCounts{Input: 10, Output: 20, CacheRead: 30, CacheWrite5m: 4},
					CostUSD:       1.234,
					Priced:        true,
					CacheHitRatio: &ratio,
				},
			},
			{
				Key: "PART",
				Summary: usage.Summary{
					Tokens:  domain.TokenCounts{Input: 1, Output: 2, CacheRead: 3, CacheWrite5m: 4},
					CostUSD: 1.234,
					Priced:  true,
					Partial: true,
				},
			},
			{
				Key: "gpt-5.6-terra",
				Summary: usage.Summary{
					Tokens: domain.TokenCounts{
						Input:        12_700,
						Output:       100,
						CacheRead:    1_300_000,
						CacheWrite5m: 1_500,
						CacheWrite1h: 500,
					},
					Priced: false,
				},
			},
		},
		Unpriced: []string{"gpt-5.6-terra"},
	}
	var buf bytes.Buffer
	if err := formatUsageReport(&buf, rep); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) < 5 {
		t.Fatalf("lines = %q", lines)
	}
	const header = "KEY  INPUT  OUTPUT  CACHE_READ  CACHE_WRITE  ~COST  CACHE"
	if lines[0] != header {
		t.Fatalf("header = %q", lines[0])
	}
	proj := fieldsOf(t, lines, "PROJ-1")
	if proj[5] != "~$1.23" || proj[6] != "81%" {
		t.Fatalf("PROJ-1 = %#v", proj)
	}
	part := fieldsOf(t, lines, "PART")
	if !strings.HasSuffix(part[5], "+") {
		t.Fatalf("partial cost = %q, want a + suffix", part[5])
	}
	terra := fieldsOf(t, lines, "gpt-5.6-terra")
	if terra[1] != "12.7k" || terra[2] != "100" || terra[3] != "1.3M" || terra[4] != "2.0k" || terra[5] != "-" {
		t.Fatalf("unpriced row = %#v, want tokens and a - cost", terra)
	}
	const footer = "unpriced: gpt-5.6-terra (tokens only; set usage.prices in ~/.aiman/config.yaml to see ~$)"
	if lines[len(lines)-1] != footer {
		t.Fatalf("footer = %q", lines[len(lines)-1])
	}
}

func TestFormatUsageReportTotalWhenNoRows(t *testing.T) {
	ratio := 0.5
	rep := usage.Report{Total: usage.Summary{
		Tokens:        domain.TokenCounts{Input: 12_700},
		CostUSD:       1.234,
		Priced:        true,
		CacheHitRatio: &ratio,
	}}
	var buf bytes.Buffer
	if err := formatUsageReport(&buf, rep); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q", lines)
	}
	total := fieldsOf(t, lines, "(total)")
	if total[1] != "12.7k" || total[5] != "~$1.23" || total[6] != "50%" {
		t.Fatalf("total = %#v", total)
	}
}

func fieldsOf(t *testing.T, lines []string, key string) []string {
	t.Helper()
	for _, line := range lines {
		fields := strings.Split(line, "  ")
		if len(fields) > 0 && fields[0] == key {
			if len(fields) != 7 {
				t.Fatalf("%s has %d columns: %q", key, len(fields), fields)
			}
			return fields
		}
	}
	t.Fatalf("missing row %s in %q", key, lines)
	return nil
}
