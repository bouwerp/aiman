package server

import (
	"os"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/apicompat"
)

// repoRoot is the repository root seen from this package's directory.
const repoRoot = "../.."

// holderContractTypes are the ptyhold structs written to request.json,
// meta.json and activity.json, which an older holder keeps writing after
// serve updates.
var holderContractTypes = []string{"Spec", "Meta", "Activity"}

// TestOpenAPICompatibleWithPreviousRelease holds serve to protocol 1's rule:
// against the previous release tag, the OpenAPI contract, the gateway's
// device allowlist and the holder file JSON names only gain.
func TestOpenAPICompatibleWithPreviousRelease(t *testing.T) {
	tag, ok := apicompat.PreviousTag(repoRoot)
	if !ok {
		if os.Getenv("GITHUB_ACTIONS") != "" {
			t.Fatal("no previous release tag in CI; check out with fetch-depth: 0")
		}
		t.Skip("NOTICE: no previous release tag in this checkout; fetch tags to run the compatibility check")
	}
	t.Logf("comparing against %s", tag)

	checks := []struct {
		path    string
		compare func(old, cur []byte) []string
	}{
		{"api/openapi.yaml", apicompat.CompareSpecs},
		{"internal/gateway/allowlist.go", apicompat.CompareAllowlists},
		{"internal/ptyhold/contract.go", func(old, cur []byte) []string {
			return apicompat.CompareJSONNames(old, cur, holderContractTypes)
		}},
	}
	for _, c := range checks {
		t.Run(c.path, func(t *testing.T) {
			old, err := apicompat.ShowAtTag(repoRoot, tag, c.path)
			if err != nil {
				t.Fatalf("read %s at %s: %v", c.path, tag, err)
			}
			cur, err := os.ReadFile(repoRoot + "/" + c.path)
			if err != nil {
				t.Fatal(err)
			}
			if problems := c.compare(old, cur); len(problems) > 0 {
				t.Fatalf("%s breaks clients of %s:\n%s", c.path, tag, strings.Join(problems, "\n"))
			}
		})
	}
}
