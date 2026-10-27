package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
)

func TestTakeFlagsFromStdinIsBoolean(t *testing.T) {
	flags, rest := takeFlags([]string{"--from-stdin", "--id", "n1", "ignored"})
	if flags["from-stdin"] != "1" {
		t.Fatalf("%v", flags)
	}
	if flags["id"] != "n1" {
		t.Fatalf("id %q", flags["id"])
	}
	if len(rest) != 1 || rest[0] != "ignored" {
		t.Fatalf("rest %v", rest)
	}
}

func TestSessionCreateForFlag(t *testing.T) {
	flags, _ := takeFlags([]string{"--for", "t3", "--parent", "p"})
	params, err := sessionCreateParams(flags)
	if err != nil {
		t.Fatal(err)
	}
	if params["for"] != "t3" || params["parent"] != "p" {
		t.Fatalf("params = %v, want for t3 and parent p", params)
	}
	if _, ok := params["agent"]; ok {
		t.Fatalf("params = %v, an unset agent must stay off the wire", params)
	}
}

// The brief travels as file content in the params, never on argv.
func TestSessionCreateBriefFileFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brief.md")
	const brief = "## Question\n\nWhy?\n"
	if err := os.WriteFile(path, []byte(brief), 0o600); err != nil {
		t.Fatal(err)
	}
	flags, _ := takeFlags([]string{"--for", "t3", "--brief-file", path})
	params, err := sessionCreateParams(flags)
	if err != nil {
		t.Fatal(err)
	}
	if params["brief"] != brief {
		t.Fatalf("brief = %v, want the file's content", params["brief"])
	}
	if _, ok := params["brief-file"]; ok {
		t.Fatalf("params = %v, the path must not be sent as a param", params)
	}

	flags, _ = takeFlags([]string{"--brief-file", filepath.Join(t.TempDir(), "missing.md")})
	if _, err := sessionCreateParams(flags); err == nil {
		t.Fatal("an unreadable brief file must be an error")
	}
}

func TestRunDeepcodeWatchNoopsOutsideAiman(t *testing.T) {
	t.Setenv("AIMAN_ENV", "")
	t.Setenv("AIMAN_ID", "")
	if err := runDeepcodeWatch([]string{"--dir", t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

func TestReportNativeToServeWhenDown(t *testing.T) {
	err := reportNativeToServe(filepath.Join(t.TempDir(), "aiman.sock"), "s1", agenthook.Report{Native: agenthook.Native{ID: "n"}})
	if err != nil {
		t.Fatalf("missing serve must be silent: %v", err)
	}
}

func TestReportParamsForwardsPreCompactEvent(t *testing.T) {
	got := agenthook.ExtractReport([]byte(`{"session_id":"abc","hook_event_name":"PreCompact","trigger":"auto"}`))
	params := reportParams("aim-1", got)
	if params["event"] != "PreCompact" {
		t.Fatalf("event=%v", params["event"])
	}
	if params["agent_session_id"] != "abc" {
		t.Fatalf("agent_session_id=%v", params["agent_session_id"])
	}
	if params["id"] != "aim-1" {
		t.Fatalf("id=%v", params["id"])
	}
}

func TestFormatSessionEvent(t *testing.T) {
	ev := domain.SessionEvent{
		Seq: 2, Kind: domain.EventHookReported, Actor: domain.EventActorAgent,
		At:      time.Date(2026, 10, 26, 20, 0, 0, 0, time.UTC),
		Payload: []byte(`{"state":"idle"}`),
	}
	got := formatSessionEvent(ev)
	for _, want := range []string{"2 hook.reported agent 2026-10-26T20:00:00Z", `{"state":"idle"}`} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q missing %q", got, want)
		}
	}
}

func TestFormatSessionEventTruncatesPayload(t *testing.T) {
	ev := domain.SessionEvent{Seq: 1, Kind: domain.EventHookReported, Payload: []byte(strings.Repeat("x", 200))}
	got := formatSessionEvent(ev)
	if !strings.HasSuffix(got, "...") || len(got) > 200 {
		t.Fatalf("payload not truncated: %q", got)
	}
}
