package harness

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite the compose goldens under testdata/compose")

// checkImageFixture adds a check with its own image to composeFixture.
func checkImageFixture(file string) string {
	return file + `    checks:
      - {name: unit, run: "go test ./..."}
      - {name: lint, run: "npm run lint", image: "node:24"}
`
}

// TestComposeGolden pins the harness compose output byte for byte for a
// plain run, a check image, Go and Node debug runs and the capture proxy
// offline and online.
func TestComposeGolden(t *testing.T) {
	nodeFile := debugFixture(nodeDebugBlock)
	cases := []struct {
		name string
		file string
		spec func(t *testing.T) RunSpec
	}{
		{"plain", composeFixture, func(*testing.T) RunSpec { return testSpec() }},
		{"check-image", checkImageFixture(composeFixture), func(*testing.T) RunSpec {
			s := testSpec()
			s.CheckImages = map[string]string{CheckService(1): "sha256:checkimage"}
			s.ServiceImages = map[string]string{"db": "sha256:postgres"}
			return s
		}},
		{"debug-go", debugFixture("go"), func(*testing.T) RunSpec { return debugSpec() }},
		{"debug-node", nodeFile, func(t *testing.T) RunSpec { return nodeSpec(t, nodeFile) }},
		{"proxy-offline", composeFixture, func(*testing.T) RunSpec { return proxySpec(false) }},
		{"proxy-online", composeFixture, func(*testing.T) RunSpec { return proxySpec(true) }},
		{"aws", awsFixture, func(*testing.T) RunSpec {
			s := awsSpec()
			s.Proxy = proxySpec(false).Proxy
			return s
		}},
		{"proxy-debug-node", nodeFile, func(t *testing.T) RunSpec {
			s := nodeSpec(t, nodeFile)
			s.Proxy = proxySpec(true).Proxy
			return s
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := buildDebug(t, tc.file, tc.spec(t))
			checkGolden(t, filepath.Join("testdata", "compose", tc.name+".yaml"), p.YAML)
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
