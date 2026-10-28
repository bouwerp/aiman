package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestParseAgentsArgs(t *testing.T) {
	cases := []struct {
		args   []string
		method string
		params map[string]any
		json   bool
	}{
		{[]string{"status"}, "agents.status", map[string]any{}, false},
		{[]string{"status", "--json", "--refresh"}, "agents.status", map[string]any{"refresh": true}, true},
		{[]string{"update", "claude"}, "agents.update", map[string]any{"agent": "claude"}, false},
		{[]string{"update", "--all", "--json"}, "agents.update", map[string]any{"all": true}, true},
	}
	for _, c := range cases {
		got, err := parseAgentsArgs(c.args)
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if got.method != c.method || !reflect.DeepEqual(got.params, c.params) || got.json != c.json {
			t.Fatalf("%v: %+v", c.args, got)
		}
	}
	for _, bad := range [][]string{
		nil,
		{"nope"},
		{"status", "extra"},
		{"status", "--all"},
		{"update"},
		{"update", "claude", "--all"},
		{"update", "claude", "codex"},
		{"update", "--refresh", "claude"},
	} {
		if _, err := parseAgentsArgs(bad); err == nil {
			t.Fatalf("%v: want error", bad)
		}
	}
}

func TestFormatAgentsStatus(t *testing.T) {
	var buf bytes.Buffer
	err := formatAgentsStatus(&buf, map[string]any{
		"type": "agents", "host": "regent", "mode": "auto", "window": "03:00-06:00",
		"agents": []any{
			map[string]any{"agent": "claude", "installed": "2.1.312", "latest": "2.1.315", "method": "native", "status": "update available"},
			map[string]any{"agent": "muse", "installed": "1.4.13", "method": "managed", "status": "latest unknown", "held": true},
			map[string]any{"agent": "pi", "status": "not installed"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"regent", "mode auto", "03:00-06:00", "AGENT", "claude", "2.1.315", "update available", "latest unknown (held)", "pi", "not installed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
}

func TestFormatAgentsUpdate(t *testing.T) {
	var buf bytes.Buffer
	refused := formatAgentsUpdate(&buf, map[string]any{
		"type": "agents_update",
		"results": []any{
			map[string]any{"agent": "claude", "accepted": true},
			map[string]any{"agent": "codex", "accepted": false, "reason": "deferred: 1 session busy"},
		},
	})
	out := buf.String()
	if refused != 1 || !strings.Contains(out, "claude: update started") || !strings.Contains(out, "codex: deferred: 1 session busy") {
		t.Fatalf("refused %d\n%s", refused, out)
	}
	buf.Reset()
	if formatAgentsUpdate(&buf, map[string]any{"results": []any{}}) != 0 || !strings.Contains(buf.String(), "no outdated agents") {
		t.Fatalf("empty: %s", buf.String())
	}
}
