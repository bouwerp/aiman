package console

import (
	"regexp"
	"testing"
)

// TestPageIDsUnique guards the scripts' getElementById lookups: with a
// duplicate id the second element is unreachable and the first takes its
// writes.
func TestPageIDsUnique(t *testing.T) {
	page, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`\sid="([^"]+)"`).FindAllSubmatch(page, -1) {
		id := string(m[1])
		if seen[id] {
			t.Errorf("id %q appears more than once in index.html", id)
		}
		seen[id] = true
	}
}
