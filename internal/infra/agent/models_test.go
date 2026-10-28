package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParsePiModelsOpenRouter(t *testing.T) {
	got := ParsePiModels(readFixture(t, "pi_list_models_openrouter.txt"))
	if len(got) != 19 {
		t.Fatalf("parsed %d models, want 19: %+v", len(got), got)
	}
	first := got[0]
	if first.Provider != "openrouter" || first.ID != "openrouter/~anthropic/claude-fable-latest" {
		t.Fatalf("first = %+v", first)
	}
	if !strings.Contains(first.Label, "~anthropic/claude-fable-latest") || !strings.Contains(first.Label, "1M") {
		t.Fatalf("label = %q", first.Label)
	}
	for _, m := range got {
		if m.Provider == "provider" {
			t.Fatalf("header parsed as a model: %+v", m)
		}
	}
}

func TestParsePiModelsDeepseekProviders(t *testing.T) {
	got := ParsePiModels(readFixture(t, "pi_list_models_deepseek.txt"))
	provs := ModelProviders(got)
	if len(provs) != 2 || provs[0].Name != "deepseek" || provs[0].Count != 2 || provs[1].Name != "openrouter" || provs[1].Count != 18 {
		t.Fatalf("providers = %+v", provs)
	}
	if got[0].ID != "deepseek/deepseek-flash" {
		t.Fatalf("first = %+v", got[0])
	}
	var batch bool
	for _, m := range got {
		if m.ID == "openrouter/deepseek/deepseek-v4.1-flash:batch" {
			batch = true
		}
	}
	if !batch {
		t.Fatal("a model id with a colon must survive parsing")
	}
}

func TestParsePiModelsIgnoresNoise(t *testing.T) {
	if got := ParsePiModels("\nWarning: something\n\n"); len(got) != 0 {
		t.Fatalf("noise parsed: %+v", got)
	}
}

func TestStaticModelsFromCatalog(t *testing.T) {
	got := StaticModels("claude")
	if len(got) == 0 || got[0].ID != claudeCatalog.Models[0] || got[0].Provider != "" {
		t.Fatalf("claude = %+v", got)
	}
	if HasDynamicModels("claude") || !HasDynamicModels("pi") || !HasDynamicModels(" pi --foo") {
		t.Fatal("only pi lists models from the host")
	}
	if StaticModels("unknown-agent") != nil {
		t.Fatal("unknown agents have no catalog")
	}
}

type fakeExec struct {
	out   string
	err   error
	calls []string
}

func (f *fakeExec) Execute(_ context.Context, cmd string) (string, error) {
	f.calls = append(f.calls, cmd)
	return f.out, f.err
}

func TestModelCacheListsPiOncePerHost(t *testing.T) {
	ex := &fakeExec{out: readFixture(t, "pi_list_models_deepseek.txt")}
	c := NewModelCache()
	for range 2 {
		got, err := c.List(context.Background(), "h1", ex, "pi")
		if err != nil || len(got) != 20 {
			t.Fatalf("list = %d, %v", len(got), err)
		}
	}
	if len(ex.calls) != 1 || !strings.Contains(ex.calls[0], "pi --list-models") {
		t.Fatalf("calls = %q", ex.calls)
	}
	if _, err := c.List(context.Background(), "h2", ex, "pi"); err != nil || len(ex.calls) != 2 {
		t.Fatalf("second host must list again: %v %q", err, ex.calls)
	}
	static, err := c.List(context.Background(), "h1", ex, "codex")
	if err != nil || len(static) == 0 || len(ex.calls) != 2 {
		t.Fatalf("static list ran a command: %v %q", err, ex.calls)
	}
}

func TestModelCacheErrorsAreNotCached(t *testing.T) {
	ex := &fakeExec{err: errors.New("unreachable")}
	c := NewModelCache()
	if _, err := c.List(context.Background(), "h", ex, "pi"); err == nil {
		t.Fatal("want error")
	}
	ex.err, ex.out = nil, ""
	if _, err := c.List(context.Background(), "h", ex, "pi"); err == nil {
		t.Fatal("an empty listing is an error")
	}
	ex.out = readFixture(t, "pi_list_models_openrouter.txt")
	if got, err := c.List(context.Background(), "h", ex, "pi"); err != nil || len(got) != 19 {
		t.Fatalf("retry = %d, %v", len(got), err)
	}
}
