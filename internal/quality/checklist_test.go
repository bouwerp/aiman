package quality

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestParseChecklist(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"list", "---\napplies_to: [\"**/*.go\"]\n---\n- [ ] errors wrapped\n", []string{"**/*.go"}},
		{"string", "---\napplies_to: \"*.tf\"\n---\n- [ ] plan reviewed\n", []string{"*.tf"}},
		{"no frontmatter", "- [ ] anything\n", nil},
		{"frontmatter without applies_to", "---\ntitle: x\n---\nbody\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseChecklist("go", "/h/.aiman/checklists/go.md", "user", []byte(tt.content))
			if err != nil {
				t.Fatalf("ParseChecklist: %v", err)
			}
			if !reflect.DeepEqual(got.AppliesTo, tt.want) {
				t.Errorf("AppliesTo = %#v, want %#v", got.AppliesTo, tt.want)
			}
			if got.Name != "go" || got.Path != "/h/.aiman/checklists/go.md" || got.Source != "user" {
				t.Errorf("identity fields = %+v", got)
			}
		})
	}
}

func TestParseChecklistRejectsBadFrontmatter(t *testing.T) {
	for _, content := range []string{
		"---\napplies_to: [\n---\nbody\n",
		"---\napplies_to: [\"a\"]\nbody without closing fence\n",
		"---\napplies_to: {a: b}\n---\n",
	} {
		if _, err := ParseChecklist("x", "/p/x.md", "repo", []byte(content)); err == nil {
			t.Errorf("ParseChecklist(%q) = nil error, want error", content)
		}
	}
}

func TestPathspecsForGlob(t *testing.T) {
	got := PathspecsFor([]string{"*.go", "web/**/*.ts"})
	want := []string{":(glob)*.go", ":(glob)**/*.go", ":(glob)web/**/*.ts"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PathspecsFor = %#v, want %#v", got, want)
	}
}

func TestSelectShadowsAndCaps(t *testing.T) {
	all := []domain.Checklist{
		{Name: "go", Source: "user", Path: "/home/.aiman/checklists/go.md"},
		{Name: "go", Source: "repo", Path: "/wt/.aiman/checklists/go.md", AppliesTo: []string{"*.go"}},
		{Name: "tf", Source: "repo", AppliesTo: []string{"*.tf"}},
	}
	got := Select(all, map[string]bool{"go": true})
	if len(got) != 1 || got[0].Source != "repo" || got[0].Name != "go" {
		t.Fatalf("Select shadow = %+v, want only repo go", got)
	}

	var many []domain.Checklist
	for i := 9; i >= 0; i-- {
		src := "user"
		if i%2 == 0 {
			src = "repo"
		}
		many = append(many, domain.Checklist{Name: fmt.Sprintf("c%d", i), Source: src})
	}
	got = Select(many, nil)
	if len(got) != MaxChecklists {
		t.Fatalf("len = %d, want %d", len(got), MaxChecklists)
	}
	var names []string
	for _, c := range got {
		names = append(names, c.Source+":"+c.Name)
	}
	want := []string{"repo:c0", "repo:c2", "repo:c4", "repo:c6", "repo:c8", "user:c1", "user:c3", "user:c5"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("order = %v, want %v", names, want)
	}
}
