package jira

import (
	"strings"
	"testing"
)

func TestStatusFilter_DefaultsWhenUnset(t *testing.T) {
	got := statusFilter(nil)
	for _, want := range DefaultIssueStatuses {
		if !strings.Contains(got, `"`+want+`"`) {
			t.Errorf("expected default status %q in filter, got %s", want, got)
		}
	}
	if !strings.HasPrefix(got, "status IN (") {
		t.Errorf("expected a status IN clause, got %s", got)
	}
}

func TestStatusFilter_ExcludesBacklogStatuses(t *testing.T) {
	got := statusFilter(nil)
	for _, unwanted := range []string{"To Do", "Later", "Done"} {
		if strings.Contains(got, `"`+unwanted+`"`) {
			t.Errorf("status %q must not be in the default filter, got %s", unwanted, got)
		}
	}
}

func TestStatusFilter_UsesConfiguredStatuses(t *testing.T) {
	got := statusFilter([]string{"In Development", "  ", "Dev Review"})
	want := `status IN ("In Development", "Dev Review")`
	if got != want {
		t.Errorf("expected %s, got %s", want, got)
	}
}

func TestStatusFilter_EscapesQuotes(t *testing.T) {
	got := statusFilter([]string{`Bob's "state"`})
	want := `status IN ("Bob's \"state\"")`
	if got != want {
		t.Errorf("expected %s, got %s", want, got)
	}
}

func TestBuildDefaultJQL_ScopedToMeAndStatuses(t *testing.T) {
	got := buildDefaultJQL(nil)
	if !strings.HasPrefix(got, "assignee = currentUser() AND status IN (") {
		t.Errorf("expected assignee+status scoping, got %s", got)
	}
	if !strings.HasSuffix(got, "ORDER BY created DESC") {
		t.Errorf("expected created DESC ordering, got %s", got)
	}
	// Regression: issues assigned to other people must not be pulled in.
	if strings.Contains(got, "assignee !=") || strings.Contains(got, " OR status") {
		t.Errorf("query must not widen beyond the current user, got %s", got)
	}
	if strings.Contains(got, "statusCategory") {
		t.Errorf("statusCategory is redundant with an explicit allow-list, got %s", got)
	}
}

func TestBuildSearchJQL_KeyShapedQuery(t *testing.T) {
	got := buildSearchJQL("AIMAN-42", []string{"Dev Ready"})
	want := `assignee = currentUser() AND status IN ("Dev Ready") AND (summary ~ "AIMAN-42" OR key = AIMAN-42) ORDER BY created DESC`
	if got != want {
		t.Errorf("expected\n%s\ngot\n%s", want, got)
	}
}

func TestBuildSearchJQL_ProseQueryOmitsKeyClause(t *testing.T) {
	got := buildSearchJQL("login bug", []string{"Dev Ready"})
	if strings.Contains(got, "key =") {
		t.Errorf("a non-key query must not produce a key clause (Jira rejects it), got %s", got)
	}
	if !strings.Contains(got, `summary ~ "login bug"`) {
		t.Errorf("expected a summary match, got %s", got)
	}
	if !strings.Contains(got, "assignee = currentUser()") {
		t.Errorf("search must stay scoped to the current user, got %s", got)
	}
}

func TestBuildSearchJQL_QuotesInQueryAreEscaped(t *testing.T) {
	got := buildSearchJQL(`say "hi"`, []string{"Dev Ready"})
	if !strings.Contains(got, `summary ~ "say \"hi\""`) {
		t.Errorf("expected escaped quotes in the summary match, got %s", got)
	}
}

func TestLooksLikeIssueKey(t *testing.T) {
	cases := map[string]bool{
		"AIMAN-1":     true,
		"aiman-12":    true,
		"AB2-300":     true,
		"login bug":   false,
		"AIMAN":       false,
		"AIMAN-":      false,
		"-1":          false,
		"AIMAN-1 fix": false,
		"":            false,
	}
	for in, want := range cases {
		if got := looksLikeIssueKey(in); got != want {
			t.Errorf("looksLikeIssueKey(%q) = %v, want %v", in, got, want)
		}
	}
}
