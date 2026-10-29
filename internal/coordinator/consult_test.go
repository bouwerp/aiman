package coordinator

import (
	"errors"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func allInstalled(string) bool { return true }

func TestPickConsultPrefersOtherFamily(t *testing.T) {
	smart := []config.PreferredAgent{{Agent: "claude", Model: "opus"}, {Agent: "codex", Model: "gpt-5.4"}}
	got, err := PickConsult(smart, "claude", "opus", allInstalled)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "codex" || got.Model != "gpt-5.4" {
		t.Fatalf("picked %+v, want codex/gpt-5.4", got)
	}
}

func TestPickConsultOtherModelSameFamily(t *testing.T) {
	smart := []config.PreferredAgent{{Agent: "claude", Model: "opus"}, {Agent: "claude", Model: "sonnet"}}
	got, err := PickConsult(smart, "claude", "opus", allInstalled)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "claude" || got.Model != "sonnet" {
		t.Fatalf("picked %+v, want claude/sonnet", got)
	}
}

func TestPickConsultSingleSameEntry(t *testing.T) {
	smart := []config.PreferredAgent{{Agent: "claude", Model: "opus"}}
	got, err := PickConsult(smart, "claude", "opus", allInstalled)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "claude" || got.Model != "opus" {
		t.Fatalf("picked %+v, want claude/opus", got)
	}
}

func TestPickConsultSkipsUninstalled(t *testing.T) {
	smart := []config.PreferredAgent{{Agent: "codex", Model: "gpt-5.4"}, {Agent: "claude", Model: "sonnet"}}
	got, err := PickConsult(smart, "Claude Code", "opus", func(c string) bool { return c == "claude" })
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "claude" || got.Model != "sonnet" {
		t.Fatalf("picked %+v, want claude/sonnet", got)
	}
}

func TestPickConsultEmptySmart(t *testing.T) {
	_, err := PickConsult(nil, "claude", "opus", allInstalled)
	if !errors.Is(err, ErrConsultNeedsSmart) {
		t.Fatalf("err = %v, want ErrConsultNeedsSmart", err)
	}
	_, err = PickConsult([]config.PreferredAgent{{Agent: "codex"}}, "claude", "", func(string) bool { return false })
	if !errors.Is(err, ErrConsultNeedsSmart) {
		t.Fatalf("nothing installed: err = %v, want ErrConsultNeedsSmart", err)
	}
}

func TestParseConsultReport(t *testing.T) {
	md := "# Consult\n\n## Objections\n\n- [high] The store has no lock. Add one.\n" +
		"- [HIGH] Two writers race on the file. Serialise them.\n" +
		"* [low] Naming. Rename Foo.\n- not a severity line\n\n## Verdict\n\nRevise: fix the two races.\n"
	got, err := ParseConsultReport(md)
	if err != nil {
		t.Fatal(err)
	}
	want := ConsultReport{High: 2, Medium: 0, Low: 1, Verdict: "revise"}
	if got != want {
		t.Fatalf("report = %+v, want %+v", got, want)
	}

	bad := []struct{ name, md string }{
		{"missing verdict", "## Objections\n\n- [high] x\n"},
		{"unknown verdict", "## Objections\n\n- [low] x\n\n## Verdict\n\nmaybe\n"},
		{"empty verdict", "## Verdict\n\n"},
		{"empty", ""},
	}
	for _, c := range bad {
		if _, err := ParseConsultReport(c.md); err == nil {
			t.Errorf("%s: no error", c.name)
		}
	}
}

func TestParseConsultReportNoObjections(t *testing.T) {
	got, err := ParseConsultReport("## Objections\n\nNone.\n\n## Verdict\n\napprove\n")
	if err != nil {
		t.Fatal(err)
	}
	if got != (ConsultReport{Verdict: "approve"}) {
		t.Fatalf("report = %+v", got)
	}
}

func TestParseConsultBrief(t *testing.T) {
	md := "## Question\n\nWhat is wrong with this design?\n\n## Scope\n\nRead only.\n\n## Report must contain\n\nObjections and a verdict.\n"
	b, err := ParseConsultBrief(md)
	if err != nil {
		t.Fatal(err)
	}
	if b.Question != "What is wrong with this design?" || b.Scope != "Read only." || b.Report != "Objections and a verdict." {
		t.Fatalf("brief = %+v", b)
	}
	if b.Markdown() != md {
		t.Fatalf("markdown = %q", b.Markdown())
	}
	for _, bad := range []string{
		"## Question\n\nwhat is wrong here with it\n\n## Scope\n\nx\n",
		"## Question\n\na\n\n## Scope\n\nb\n\n## Report must contain\n\nc\n",
		"## Question\n\nq q q q\n\n## Scope\n\ns s s\n\n## Report must contain\n\nr r\n\n## Owns\n\nx\n",
	} {
		if _, err := ParseConsultBrief(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
