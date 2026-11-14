//go:build pileup_b

package main

// Contract:
//
//   - resources.get's result gains `unscoped: [{id, name}]`
//     (server.ResourcesReport.Unscoped []server.UnscopedSession) and
//     `serve_unit` (cgroup.Snapshot.ServeUnit *cgroup.Usage), which
//     printResources reads.
//   - printResources prints, whatever the session scope list holds:
//   - a line starting "serve unit:" with serve's own cgroup memory
//     rendered by memoryOfLimit (cgroup.FormatSize of memory_current when
//     it has no memory_high);
//   - one line per unscoped row naming the session (its name, else its
//     id) and containing "restart to move its memory and scratch".

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/cgroup"
)

func unscopedReport(sessions []map[string]any) map[string]any {
	return map[string]any{
		"type": "resources",
		"resources": map[string]any{
			"supported":      true,
			"host":           map[string]any{"memory": 3, "cpu": 10, "io": 1},
			"user_slice":     map[string]any{"memory_current": 20 << 30, "memory_high": 24 << 30},
			"sessions_slice": map[string]any{"memory_current": 4 << 30},
			"serve_unit":     map[string]any{"memory_current": 13 << 30},
			"sessions":       sessions,
			"limits":         map[string]any{"memory_high": 6 << 30},
			"unscoped": []map[string]any{
				{"id": "1f2e3d4c-legacy", "name": "legacy-one"},
				{"id": "9a8b7c6d-noname"},
			},
		},
	}
}

func lineWith(out, sub string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func TestAcceptanceResourcesPrintsUnscopedServeUnitAndRestartHint(t *testing.T) {
	scoped := []map[string]any{{"id": "s1", "name": "scoped-one", "memory_current": 1 << 30, "memory_high": 6 << 30}}
	for name, sessions := range map[string][]map[string]any{"with scopes": scoped, "no scopes": {}} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := printResources(&out, unscopedReport(sessions)); err != nil {
				t.Fatalf("printResources: %v", err)
			}
			text := out.String()

			serve := ""
			for _, l := range strings.Split(text, "\n") {
				if strings.HasPrefix(l, "serve unit:") {
					serve = l
				}
			}
			if serve == "" || !strings.Contains(serve, cgroup.FormatSize(13<<30)) {
				t.Fatalf("no serve unit line with %s:\n%s", cgroup.FormatSize(13<<30), text)
			}

			for _, label := range []string{"legacy-one", "9a8b7c6d-noname"} {
				l := lineWith(text, label)
				if l == "" {
					t.Fatalf("no row for unscoped session %s:\n%s", label, text)
				}
				if !strings.Contains(l, "restart to move its memory and scratch") {
					t.Fatalf("row for %s has no restart hint: %q\n%s", label, l, text)
				}
			}
		})
	}
}
