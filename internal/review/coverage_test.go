package review

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestChangedLines(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "coverage", "diff.patch"))
	if err != nil {
		t.Fatal(err)
	}
	got := ChangedLines(string(raw))
	want := []Hunk{
		{Path: "pkg/a.go", Lines: []int{3, 4}},
		{Path: "new.js", Lines: []int{2}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChangedLines = %#v, want %#v", got, want)
	}
	for _, h := range got {
		if h.Path == "gone.go" || h.Path == "old.js" {
			t.Fatalf("deletions/old rename path leaked: %#v", got)
		}
	}
}

func TestGoCoverage(t *testing.T) {
	cov, err := GoCoverage(filepath.Join("testdata", "coverage", "cover.profile"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cov["/src/pkg/a.go"]; ok {
		t.Fatalf("left /src prefix on keys: %v", keysOf(cov))
	}
	a := cov["pkg/a.go"]
	if !a[3] {
		t.Fatalf("pkg/a.go:3 covered=%v, want true", a[3])
	}
	if a[6] {
		t.Fatalf("pkg/a.go:6 covered=%v, want false", a[6])
	}
	if !cov["github.com/example/mod/other.go"][1] {
		t.Fatal("unprefixed profile path dropped")
	}
	reloc := RelocateCoverage(cov, "github.com/example/mod")
	if !reloc["other.go"][1] {
		t.Fatalf("RelocateCoverage keys %v", keysOf(reloc))
	}
}

func TestIstanbulCoverage(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "coverage", "coverage-final.json"))
	if err != nil {
		t.Fatal(err)
	}
	cov, err := IstanbulCoverage(raw, "/src")
	if err != nil {
		t.Fatal(err)
	}
	u := cov["lib/util.js"]
	if u == nil {
		t.Fatalf("keys %v, want lib/util.js", keysOf(cov))
	}
	if !u[1] {
		t.Fatalf("lib/util.js:1 covered=%v, want true", u[1])
	}
	if u[2] {
		t.Fatalf("lib/util.js:2 covered=%v, want false", u[2])
	}
}

func TestCoverageReport(t *testing.T) {
	h := []Hunk{{Path: "pkg/a.go", Lines: []int{3, 4, 6}}}
	cov := map[string]map[int]bool{"pkg/a.go": {3: true, 4: true, 6: false}}
	md, ran, missed := CoverageReport(h, cov)
	if ran != 2 || missed != 1 {
		t.Fatalf("totals ran=%d missed=%d, want 2 and 1", ran, missed)
	}
	if !strings.Contains(md, "pkg/a.go") {
		t.Fatalf("report missing path:\n%s", md)
	}
	if !strings.Contains(md, "missed: 6") {
		t.Fatalf("report missing missed line 6:\n%s", md)
	}
	if !strings.Contains(md, "ran: 2") || !strings.Contains(md, "missed: 1") {
		t.Fatalf("report missing totals:\n%s", md)
	}
}

func keysOf(m map[string]map[int]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
