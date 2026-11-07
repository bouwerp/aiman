//go:build acceptance

package domain

import (
	"os/exec"
	"testing"
)

var validBranchNames = []string{
	"feat/x-1",
	"WTB-123-fix",
	"main",
	"release/v0.29.3",
	"a.b",
	"user/topic/sub",
	"fix_underscore",
	"aiman@pr-12",
}

var invalidBranchNames = map[string]string{
	"empty":             "",
	"leading dash":      "-rf",
	"leading dash opt":  "--upload-pack=touch /tmp/x",
	"double dot":        "a..b",
	"at brace":          "a@{1}",
	"lone at":           "@",
	"space":             "a b",
	"tab":               "a\tb",
	"newline":           "a\nb",
	"nul":               "a\x00b",
	"del":               "a\x7fb",
	"tilde":             "a~1",
	"caret":             "a^",
	"colon":             "a:b",
	"question":          "a?",
	"star":              "a*",
	"bracket":           "a[b",
	"backslash":         `a\b`,
	"lock suffix":       "topic.lock",
	"segment lock":      "a.lock/b",
	"trailing slash":    "a/",
	"leading slash":     "/a",
	"trailing dot":      "a.",
	"consecutive slash": "a//b",
	"segment dot start": "a/.b",
	"dot start":         ".a",
}

func TestValidBranchNameAcceptsOrdinaryNames(t *testing.T) {
	for _, name := range validBranchNames {
		if err := ValidBranchName(name); err != nil {
			t.Errorf("ValidBranchName(%q) = %v, want nil", name, err)
		}
	}
}

func TestValidBranchNameRejectsWhatGitRejects(t *testing.T) {
	for label, name := range invalidBranchNames {
		if err := ValidBranchName(name); err == nil {
			t.Errorf("%s: ValidBranchName(%q) = nil, want an error", label, name)
		}
	}
}

// The pure-Go validator must not be looser than git itself. Skipped when git
// is absent; a name git accepts may still be refused here (a leading dash is
// refused even where git's check would allow it as a ref).
func TestValidBranchNameAgreesWithGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	all := append([]string{}, validBranchNames...)
	for _, n := range invalidBranchNames {
		all = append(all, n)
	}
	for _, name := range all {
		if name == "" || containsNUL(name) {
			continue
		}
		gitOK := exec.Command(git, "check-ref-format", "--branch", name).Run() == nil // #nosec G204 -- fixed test inputs
		if !gitOK && ValidBranchName(name) == nil {
			t.Errorf("git rejects %q but ValidBranchName accepts it", name)
		}
	}
}

func containsNUL(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return true
		}
	}
	return false
}
