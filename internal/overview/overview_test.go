package overview

import (
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/transcript"
)

func TestBuildFencesRedactsAndOrders(t *testing.T) {
	at := time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC)
	inputs := []Input{
		{Session: domain.Session{ID: "b1", Name: "zeta", Group: "g"}, State: "idle"},
		{
			Session: domain.Session{ID: "a1", Name: "alpha", Group: "g", Branch: "feat/x", AgentName: "claude"},
			State:   "working",
			Todos:   domain.TodoList{Items: []domain.TodoItem{{ID: "t1", Text: "add the parser\n## Fake heading", State: domain.TodoActive}}},
			Events: []domain.SessionEvent{
				{At: at, Actor: domain.EventActorAiman, Kind: domain.EventChildLanded, Payload: []byte(`{"sha":"ghp_abcdefghijklmnopqrstuvwxyz0123456789"}`)},
			},
			Items: []transcript.Item{
				{Kind: transcript.KindUser, At: at, Text: "use token ghp_abcdefghijklmnopqrstuvwxyz0123456789 please"},
				{Kind: transcript.KindAssistant, At: at, Text: "ok"},
				{Kind: transcript.KindToolCall, At: at, ToolCall: &transcript.ToolCall{Name: "Bash", Status: "ok", Input: "cat secret.env"}},
				{Kind: transcript.KindThinking, Text: "private reasoning"},
			},
		},
	}
	b := Build(inputs, "n0nce", []string{"exact-secret"})
	if !strings.HasPrefix(b.Text, "UNTRUSTED DATA n0nce\n") || !strings.HasSuffix(b.Text, "END UNTRUSTED DATA n0nce\n") {
		t.Fatalf("not fenced:\n%s", b.Text)
	}
	if strings.Index(b.Text, "alpha") > strings.Index(b.Text, "zeta") {
		t.Fatal("sessions not ordered by name")
	}
	for _, leak := range []string{"ghp_abcdefghijklmnopqrstuvwxyz0123456789", "cat secret.env", "private reasoning", "\n## Fake heading"} {
		if strings.Contains(b.Text, leak) {
			t.Errorf("bundle holds %q", leak)
		}
	}
	for _, want := range []string{"- branch: feat/x", "- state: working", "- 11-20 07:00 aiman child.landed", "[active] t1 add the parser ## Fake heading", "user: use token", "tool: Bash (ok)"} {
		if !strings.Contains(b.Text, want) {
			t.Errorf("bundle lacks %q:\n%s", want, b.Text)
		}
	}
	if b.Redacted == 0 || len(b.Sessions) != 2 {
		t.Fatalf("redacted %d sessions %v", b.Redacted, b.Sessions)
	}
}

func TestBuildCapsSessionsAndBundle(t *testing.T) {
	long := strings.Repeat("word ", 1000)
	var items []transcript.Item
	for range 60 {
		items = append(items, transcript.Item{Kind: transcript.KindAssistant, Text: long})
	}
	var inputs []Input
	for i := range 60 {
		inputs = append(inputs, Input{Session: domain.Session{ID: string(rune('a'+i%26)) + string(rune('a'+i/26)), Name: "s" + string(rune('a'+i%26)) + string(rune('a'+i/26))}, Items: items})
	}
	b := Build(inputs, "n", nil)
	if len(b.Text) > BundleCap+4096 {
		t.Fatalf("bundle %d bytes", len(b.Text))
	}
	if len(b.NamesOnly) == 0 || !strings.Contains(b.Text, "## Left out (bundle full)") {
		t.Fatal("no session was left out")
	}
}

func TestParseReport(t *testing.T) {
	good := "# Overview\n\n## In progress\n- alpha: adding the parser\n- zeta: idle\n\n## Needs you\n- none\n\n## Next\n1. alpha: open the PR\n2. new session: docs\n\n## Risks\n* zeta and alpha both touch config.go\n"
	r, err := ParseReport(good)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.InProgress) != 2 || len(r.NeedsYou) != 0 || len(r.Next) != 2 || r.Next[1] != "new session: docs" || len(r.Risks) != 1 {
		t.Fatalf("%+v", r)
	}
	for name, md := range map[string]string{
		"missing":   "## In progress\n- a: b\n## Next\n- x\n",
		"twice":     good + "\n## Risks\n- again\n",
		"empty":     "## In progress\n\n## Needs you\n- none\n## Next\n- x\n## Risks\n- none\n",
		"too large": strings.Repeat("x", ReportMaxBytes+1),
	} {
		if _, err := ParseReport(md); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestBriefNamesTheFilesAndTheFence(t *testing.T) {
	b := Brief("n0nce", "/x/run")
	for _, want := range []string{"/x/run/overview.md", "UNTRUSTED DATA n0nce", "## In progress", "## Needs you", "## Next", "## Risks"} {
		if !strings.Contains(b, want) {
			t.Errorf("brief lacks %q", want)
		}
	}
}
