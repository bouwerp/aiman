// Package overview builds the bundle an overview agent reads and parses the
// report it writes. An overview run is one hidden agent session that reads
// every visible session's facts, to-do list and transcript tail on a host and
// writes what is in progress, what needs the operator, and what comes next.
package overview

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/redact"
	"github.com/bouwerp/aiman/internal/transcript"
)

// Bundle limits: one session's section and the whole bundle, in bytes, and
// the transcript items and characters kept per item.
const (
	SessionCap      = 64 << 10
	BundleCap       = 2 << 20
	TranscriptItems = 40
	ItemCap         = 2 << 10
	// LedgerEvents is how many of a session's newest ledger rows the bundle
	// lists, by kind only.
	LedgerEvents = 20
)

// File names inside a run's directory.
const (
	BundleFile = "sessions.md"
	ReportFile = "overview.md"
)

// Input is what serve knows about one visible session.
type Input struct {
	Session domain.Session
	// State is the agent state serve last sampled (working, idle, waiting_input…).
	State string
	Todos domain.TodoList
	// Items are the newest transcript items, oldest first.
	Items []transcript.Item
	// Events are the newest ledger rows, oldest first. Only their time, actor
	// and kind reach the bundle, never the payload.
	Events []domain.SessionEvent
}

// Bundle is the bundle text and what it left out.
type Bundle struct {
	Text string
	// Sessions are the ids written in full.
	Sessions []string
	// NamesOnly are sessions past the bundle cap, listed by name only.
	NamesOnly []string
	// Redacted counts lines redact.Lines changed.
	Redacted int
}

// NewNonce returns the random fence marker for one bundle.
func NewNonce() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Build writes every input into one fenced bundle, sessions ordered by group
// then name. exact are values redact.Lines removes wherever they appear.
func Build(inputs []Input, nonce string, exact []string) Bundle {
	sorted := append([]Input(nil), inputs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i].Session, sorted[j].Session
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		return nameOf(a) < nameOf(b)
	})
	var out Bundle
	var body strings.Builder
	for _, in := range sorted {
		section, redacted := sessionSection(in, exact)
		section = capText(section, SessionCap)
		if body.Len()+len(section) > BundleCap {
			out.NamesOnly = append(out.NamesOnly, nameOf(in.Session))
			continue
		}
		body.WriteString(section)
		out.Sessions = append(out.Sessions, in.Session.ID)
		out.Redacted += redacted
	}
	if len(out.NamesOnly) > 0 {
		body.WriteString("## Left out (bundle full)\n\n")
		for _, n := range out.NamesOnly {
			body.WriteString("- " + oneLine(n) + "\n")
		}
	}
	out.Text = fence(nonce, body.String())
	return out
}

func fence(nonce, text string) string {
	return "UNTRUSTED DATA " + nonce + "\n" + text + "END UNTRUSTED DATA " + nonce + "\n"
}

func sessionSection(in Input, exact []string) (string, int) {
	s := in.Session
	var lines []string
	add := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	add("## Session %s (%s)", oneLine(nameOf(s)), s.ID)
	add("")
	field := func(label, value string) {
		if strings.TrimSpace(value) != "" {
			add("- %s: %s", label, oneLine(value))
		}
	}
	field("group", s.Group)
	field("parent", s.ParentID)
	field("role", string(s.Role))
	field("repo", s.RepoName)
	field("branch", s.Branch)
	field("issue", s.IssueKey)
	field("agent", s.AgentName)
	field("state", in.State)
	field("reviewing PR", s.ReviewPR)
	field("fixing PR", s.FixPR)
	add("")
	add("### To-do list")
	if len(in.Todos.Items) == 0 {
		add("- none")
	}
	for _, it := range in.Todos.Items {
		add("- [%s] %s %s", it.State, it.ID, oneLine(it.Text))
	}
	add("")
	add("### Recent events (oldest first)")
	events := in.Events
	if len(events) > LedgerEvents {
		events = events[len(events)-LedgerEvents:]
	}
	if len(events) == 0 {
		add("- none")
	}
	for _, ev := range events {
		add("- %s %s %s", ev.At.UTC().Format("01-02 15:04"), oneLine(string(ev.Actor)), oneLine(string(ev.Kind)))
	}
	add("")
	add("### Transcript tail (oldest first)")
	items := in.Items
	if len(items) > TranscriptItems {
		items = items[len(items)-TranscriptItems:]
	}
	if len(items) == 0 {
		add("- none")
	}
	for _, it := range items {
		if line := itemLine(it); line != "" {
			add("%s", line)
		}
	}
	add("")
	clean, n := redact.Lines(lines, exact)
	return strings.Join(clean, "\n") + "\n", n
}

// itemLine is one transcript item as one bundle line: words people and agents
// wrote, and only the names of the tools an agent called.
func itemLine(it transcript.Item) string {
	when := ""
	if !it.At.IsZero() {
		when = it.At.UTC().Format("01-02 15:04") + " "
	}
	switch it.Kind {
	case transcript.KindUser:
		return "- " + when + "user: " + oneLine(capText(it.Text, ItemCap))
	case transcript.KindAssistant:
		return "- " + when + "agent: " + oneLine(capText(it.Text, ItemCap))
	case transcript.KindToolCall:
		if it.ToolCall == nil {
			return ""
		}
		return "- " + when + "tool: " + oneLine(it.ToolCall.Name) + " (" + oneLine(it.ToolCall.Status) + ")"
	case transcript.KindSubagent:
		if it.Subagent == nil {
			return ""
		}
		return "- " + when + "sub-agent " + oneLine(it.Subagent.Description) + ": " + oneLine(it.Subagent.State)
	case transcript.KindSystem:
		return "- " + when + "system: " + oneLine(capText(it.Text, ItemCap))
	case transcript.KindPane:
		return "- screen: " + oneLine(capText(it.Text, ItemCap))
	}
	return ""
}

func nameOf(s domain.Session) string {
	for _, n := range []string{s.Name, s.TerminalName} {
		if n != "" {
			return n
		}
	}
	return s.ID
}

// oneLine folds newlines and runs of space so a value cannot start a heading
// or a bullet of its own.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// capText cuts s to at most n bytes on a rune boundary.
func capText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	for !utf8.ValidString(cut) && cut != "" {
		cut = cut[:len(cut)-1]
	}
	return cut + " […]"
}
