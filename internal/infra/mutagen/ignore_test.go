package mutagen

import (
	"slices"
	"testing"
)

func TestResolveIgnores(t *testing.T) {
	tests := []struct {
		name        string
		extra       []string
		useDefaults bool
		wantPresent []string
		wantAbsent  []string
	}{
		{
			name:        "defaults cover the directories that dominate transfer time",
			useDefaults: true,
			wantPresent: []string{"node_modules", "target", "dist", "build", ".venv", "__pycache__"},
			// The local mirror has to stay a usable git checkout.
			wantAbsent: []string{".git"},
		},
		{
			name:        "disabling defaults mirrors everything",
			useDefaults: false,
			wantAbsent:  []string{"node_modules", "target", "dist"},
		},
		{
			name:        "user patterns extend the defaults",
			extra:       []string{"tmp", "*.iso"},
			useDefaults: true,
			wantPresent: []string{"node_modules", "tmp", "*.iso"},
		},
		{
			name:        "bang prefix opts a default back in",
			extra:       []string{"!dist", "!build"},
			useDefaults: true,
			wantPresent: []string{"node_modules", "target"},
			wantAbsent:  []string{"dist", "build"},
		},
		{
			name:        "blank entries are dropped and duplicates collapse",
			extra:       []string{"", "   ", "node_modules", "tmp"},
			useDefaults: true,
			wantPresent: []string{"tmp"},
		},
		{
			name:        "user patterns alone when defaults are off",
			extra:       []string{"secrets"},
			useDefaults: false,
			wantPresent: []string{"secrets"},
			wantAbsent:  []string{"node_modules"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveIgnores(tt.extra, tt.useDefaults)
			for _, want := range tt.wantPresent {
				if !slices.Contains(got, want) {
					t.Errorf("expected %q in ignores, got %v", want, got)
				}
			}
			for _, bad := range tt.wantAbsent {
				if slices.Contains(got, bad) {
					t.Errorf("did not expect %q in ignores, got %v", bad, got)
				}
			}
			seen := make(map[string]bool, len(got))
			for _, p := range got {
				if seen[p] {
					t.Errorf("duplicate ignore pattern %q in %v", p, got)
				}
				seen[p] = true
			}
		})
	}
}

func TestResolveIgnoresDeduplicatesUserEntryMatchingDefault(t *testing.T) {
	got := resolveIgnores([]string{"node_modules"}, true)
	count := 0
	for _, p := range got {
		if p == "node_modules" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected node_modules exactly once, got %d in %v", count, got)
	}
}

func TestNewEngineAppliesDefaultIgnores(t *testing.T) {
	if len(NewEngine().ignores) == 0 {
		t.Fatal("NewEngine should exclude the default set, got none")
	}
	if len(NewEngineWithIgnores(nil, false).ignores) != 0 {
		t.Fatal("NewEngineWithIgnores(nil, false) should mirror everything")
	}
}
