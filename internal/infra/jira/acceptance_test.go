package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestExtractAcceptanceCriteria(t *testing.T) {
	var many strings.Builder
	many.WriteString("## AC\n")
	for i := 1; i <= 25; i++ {
		fmt.Fprintf(&many, "- item %d\n", i)
	}
	wantMany := make([]string, 20)
	for i := range wantMany {
		wantMany[i] = fmt.Sprintf("item %d", i+1)
	}

	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"heading", "## Acceptance Criteria\n\n- A\n- [ ] B\n\n## Notes\n- C", []string{"A", "B"}},
		{"bold label ordered", "**Acceptance criteria:**\n1. X\n2. Y", []string{"X", "Y"}},
		{"no heading", "Just a description\n- not AC", nil},
		{"definition of done", "Intro\n\nDefinition of done\n* [x] shipped\n+ docs", []string{"shipped", "docs"}},
		{"cap at 20", many.String(), wantMany},
		{"empty", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractAcceptanceCriteria(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExtractAcceptanceCriteria() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCustomFieldAcceptanceCriteria(t *testing.T) {
	const field = "customfield_10035"
	p := NewProvider(Config{AcceptanceCriteriaField: field})
	adf := `{"type":"doc","content":[{"type":"bulletList","content":[
		{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"A"}]}]},
		{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"B"}]}]}]}]}`
	desc := `"## Acceptance criteria\n- from description"`

	cases := []struct {
		name  string
		value string
		want  []string
	}{
		{"adf", adf, []string{"A", "B"}},
		{"plain string", `"one\ntwo"`, []string{"one", "two"}},
		{"string array", `["first", " second "]`, []string{"first", "second"}},
		{"null falls back to description", `null`, []string{"from description"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"key":"K-1","fields":{"summary":"s","description":%s,%q:%s}}`, desc, field, tc.value)
			var ji jiraIssue
			if err := json.Unmarshal([]byte(raw), &ji); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := p.toDomainIssue(ji).AcceptanceCriteria
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("AcceptanceCriteria = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFetchIssuesRequestsCustomField(t *testing.T) {
	var fields string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields = r.URL.Query().Get("fields")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"issues":[]}`)
	}))
	defer server.Close()

	p := NewProvider(Config{URL: server.URL, AcceptanceCriteriaField: "customfield_10035"})
	if _, err := p.SearchIssues(context.Background(), ""); err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if !strings.Contains(fields, "customfield_10035") {
		t.Fatalf("fields = %q, want it to include customfield_10035", fields)
	}
	if !strings.Contains(fields, "issuetype") {
		t.Fatalf("fields = %q, want the default fields kept", fields)
	}
}
