package review

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLaunchConfigGo(t *testing.T) {
	const wt = "/tmp/pr-worktree"
	raw, err := LaunchConfig("go", DebugPorts{Debug: 2345}, wt, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := findAttach(t, raw, "aiman: attach go")
	if got, _ := cfg["request"].(string); got != "attach" {
		t.Fatalf("request = %v, want attach", cfg["request"])
	}
	if port := jsonInt(cfg["port"]); port != 2345 {
		t.Fatalf("port = %v, want 2345", cfg["port"])
	}
	if !hasSubstitute(cfg, wt, "/src") {
		t.Fatalf("substitutePath does not map %s -> /src: %v", wt, cfg["substitutePath"])
	}
}

func TestLaunchConfigNode(t *testing.T) {
	const wt = "/tmp/pr-worktree"
	raw, err := LaunchConfig("node", DebugPorts{Debug: 9229}, wt, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := findAttach(t, raw, "aiman: attach node")
	if got, _ := cfg["request"].(string); got != "attach" {
		t.Fatalf("request = %v, want attach", cfg["request"])
	}
	if port := jsonInt(cfg["port"]); port != 9229 {
		t.Fatalf("port = %v, want 9229", cfg["port"])
	}
	if got, _ := cfg["localRoot"].(string); got != wt {
		t.Fatalf("localRoot = %v, want %s", cfg["localRoot"], wt)
	}
	if got, _ := cfg["remoteRoot"].(string); got != "/src" {
		t.Fatalf("remoteRoot = %v, want /src", cfg["remoteRoot"])
	}
}

func TestLaunchConfigMerges(t *testing.T) {
	existing := []byte(`{"version":"0.2.0","configurations":[{"name":"keep-me","type":"node","request":"launch"}]}`)
	raw, err := LaunchConfig("go", DebugPorts{Debug: 2345}, "/wt", existing)
	if err != nil {
		t.Fatal(err)
	}
	_ = findAttach(t, raw, "keep-me")
	_ = findAttach(t, raw, "aiman: attach go")
}

func TestDebugInitFromReadingOrder(t *testing.T) {
	got := InitFromReadingOrder([]byte("internal/foo.go:12\n\npkg/bar.go:3\n"))
	first := "break internal/foo.go:12"
	second := "break pkg/bar.go:3"
	i1, i2 := strings.Index(got, first), strings.Index(got, second)
	if i1 < 0 || i2 < 0 {
		t.Fatalf("init %q missing %q or %q", got, first, second)
	}
	if i1 > i2 {
		t.Fatalf("breakpoints out of order in %q", got)
	}
	if empty := InitFromReadingOrder(nil); empty != "" {
		t.Fatalf("empty reading order = %q, want empty", empty)
	}
}

func findAttach(t *testing.T, raw []byte, name string) map[string]any {
	t.Helper()
	var doc struct {
		Configurations []map[string]any `json:"configurations"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("launch.json: %v\n%s", err, raw)
	}
	for _, cfg := range doc.Configurations {
		if n, _ := cfg["name"].(string); n == name {
			return cfg
		}
	}
	t.Fatalf("no configuration named %q in %s", name, raw)
	return nil
}

func hasSubstitute(cfg map[string]any, from, to string) bool {
	paths, _ := cfg["substitutePath"].([]any)
	for _, p := range paths {
		m, _ := p.(map[string]any)
		if m["from"] == from && m["to"] == to {
			return true
		}
	}
	return false
}

func jsonInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}
