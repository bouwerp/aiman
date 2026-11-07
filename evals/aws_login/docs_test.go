package awslogineval

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readRepoDoc(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestEvalAWSLoginDocs checks P7's docs: AGENTS.md names the aws.* methods, the
// gateway's allow_destructive rule for aws.login_start and the permission set
// helper; README.md has a permission set section with its name and duration.
func TestEvalAWSLoginDocs(t *testing.T) {
	agents := readRepoDoc(t, "AGENTS.md")
	for _, want := range []string{"`aws.status", "`aws.login_start", "`aws.login_status", "aiman aws permission-set-policy"} {
		if !strings.Contains(agents, want) {
			t.Errorf("AGENTS.md does not name %s", want)
		}
	}
	destructive := regexp.MustCompile("`aws\\.login_start`[^.\\n]*`allow_destructive`")
	if !destructive.MatchString(agents) {
		t.Error("AGENTS.md does not say aws.login_start needs `allow_destructive` in one sentence")
	}

	readme := readRepoDoc(t, "README.md")
	heading := regexp.MustCompile(`(?mi)^#+ .*permission set.*$`)
	loc := heading.FindStringIndex(readme)
	if loc == nil {
		t.Fatal("README.md has no permission set section")
	}
	section := readme[loc[1]:]
	if next := regexp.MustCompile(`(?m)^#{1,4} `).FindStringIndex(section); next != nil {
		section = section[:next[0]]
	}
	for _, want := range []string{"aiman-delegated", "PT12H"} {
		if !strings.Contains(section, want) {
			t.Errorf("README.md permission set section does not name %s", want)
		}
	}
}
