package review

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite the compose goldens under testdata/compose")

// TestComposeGolden pins review.Compose's output byte for byte.
func TestComposeGolden(t *testing.T) {
	cases := map[string]Sandbox{
		"offline": {ID: "rev1", Dir: "/r/sandbox", Src: "/r/src", Out: "/r/out", Image: "golang:1.26"},
		"bridge-ports": {
			ID: "rev2", Dir: "/r/sandbox", Src: "/r/src", Out: "/r/out", Image: "node:22", Network: "bridge",
			Ports: []PortMap{{Host: 40001, Container: 2345}, {Host: 40002, Container: 8097}},
		},
	}
	for name, sb := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Compose(sb, 1004, 1005)
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, filepath.Join("testdata", "compose", name+".yaml"), got)
		})
	}
}

func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // test golden
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the golden\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}
