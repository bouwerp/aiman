package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// A shell an agent opened with pty create can have no name and no terminal
// name. Its row falls back to the id instead of rendering blank.
func TestUnnamedSessionRowShowsID(t *testing.T) {
	it := item{session: domain.Session{ID: "419529cf-hardhat-full"}}
	if got := it.displayName(); got != "419529cf-hardhat-full" {
		t.Fatalf("displayName = %q, want the id", got)
	}
	if got := rowName(it); got != "419529cf-hardhat-full" {
		t.Fatalf("rowName = %q, want the id", got)
	}
}

func TestSessionRowLabelPrefersNameThenTerminal(t *testing.T) {
	named := item{session: domain.Session{ID: "id", Name: "impl-x", TerminalName: "term"}}
	if got := rowName(named); got != "impl-x" {
		t.Fatalf("rowName = %q, want the name", got)
	}
	term := item{session: domain.Session{ID: "id", TerminalName: "term", IssueKey: "WTB-1"}}
	if got := term.displayName(); !strings.Contains(got, "WTB-1") || !strings.Contains(got, "term") {
		t.Fatalf("displayName = %q, want issue key and terminal", got)
	}
	if got := rowName(term); got != "term" {
		t.Fatalf("rowName = %q, want the terminal name", got)
	}
}
