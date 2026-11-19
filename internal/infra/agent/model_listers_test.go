package agent

import (
	"context"
	"strings"
	"testing"
)

func ids(opts []ModelOption) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, o.ID)
	}
	return out
}

func TestParseKiloModels(t *testing.T) {
	got := ParseProviderModels(readFixture(t, "kilo_models.txt"))
	if len(got) != 12 || got[0].ID != "kilo/~anthropic/claude-fable-latest" || got[0].Provider != "kilo" {
		t.Fatalf("kilo = %+v", got)
	}
	if n := len(ParseProviderModels("Error: not logged in\n\nkilo/a b\n")); n != 0 {
		t.Fatalf("noise parsed: %d", n)
	}
}

func TestParseCodexModelsKeepsListed(t *testing.T) {
	got := ParseCodexModels(readFixture(t, "codex_debug_models.json"))
	list := ids(got)
	if len(got) == 0 || !contains(list, "gpt-6.2-sol") || !contains(list, "gpt-6-luna") {
		t.Fatalf("codex = %v", list)
	}
	if contains(list, "gpt-reserve") || contains(list, "codex-auto-review") {
		t.Fatalf("hidden models listed: %v", list)
	}
	if !strings.Contains(got[0].Label, "GPT-6.2-Sol") {
		t.Fatalf("label %q", got[0].Label)
	}
	if ParseCodexModels("not json") != nil {
		t.Fatal("bad json parsed")
	}
}

func TestParseGrokModels(t *testing.T) {
	got := ParseGrokModels(readFixture(t, "grok_models.txt"))
	if strings.Join(ids(got), ",") != "grok-4.7,grok-4.7-build-fast,grok-4.6,grok-4.5" {
		t.Fatalf("grok = %v", ids(got))
	}
	if got[2].Label != "grok-4.6  default" {
		t.Fatalf("default label %q", got[2].Label)
	}
}

func TestParseAgyModels(t *testing.T) {
	got := ParseAgyModels(readFixture(t, "agy_models.txt"))
	if len(got) != 6 || got[0].ID != "gemini-3.8-flash-high" || got[0].Label != "gemini-3.8-flash-high  Gemini 3.8 Flash (High)" {
		t.Fatalf("agy = %+v", got)
	}
}

// TestModelCacheRunsEachListerFromHome runs the agent's own listing command
// with a time limit and no stderr.
func TestModelCacheRunsEachListerFromHome(t *testing.T) {
	for base, fixture := range map[string]string{
		"kilo": "kilo_models.txt", "codex": "codex_debug_models.json", "grok": "grok_models.txt", "agy": "agy_models.txt",
	} {
		ex := &fakeExec{out: readFixture(t, fixture)}
		got, err := NewModelCache().List(context.Background(), "h", ex, base)
		if err != nil || len(got) == 0 || !HasDynamicModels(base) {
			t.Fatalf("%s: %d %v", base, len(got), err)
		}
		cmd := ex.calls[0]
		want := "timeout 30 " + base + " " + modelListers[base].args + " 2>/dev/null"
		if !strings.Contains(cmd, want) || !strings.Contains(cmd, `cd "$HOME"`) {
			t.Fatalf("%s command %q", base, cmd)
		}
	}
}
