package hoststats

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Acceptance tests for t101: one load-average reader for the whole module.
// Do not edit.

func TestParseLoadAvg(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want float64
		ok   bool
	}{
		{"1.50 0.80 0.30 2/345 6789\n", 1.5, true},
		{"0.00 0.01 0.05 1/1 1", 0, true},
		{"", 0, false},
		{"   ", 0, false},
		{"abc 1 2", 0, false},
		{"-1 0 0", 0, false},
		{"NaN 0 0", 0, false},
		{"+Inf 0 0", 0, false},
	} {
		got, ok := ParseLoadAvg(tc.in)
		if ok != tc.ok || (ok && math.Abs(got-tc.want) > 1e-9) {
			t.Errorf("ParseLoadAvg(%q) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestReadLoadAvg(t *testing.T) {
	v, ok := ReadLoadAvg()
	if runtime.GOOS != "linux" {
		if ok {
			t.Fatalf("ReadLoadAvg on %s = %v, true; want false", runtime.GOOS, v)
		}
		return
	}
	if !ok || v < 0 {
		t.Fatalf("ReadLoadAvg = %v, %v", v, ok)
	}
}

// Only this package reads /proc/loadavg.
func TestOnlyHoststatsReadsLoadAvg(t *testing.T) {
	root := filepath.Join("..", "..")
	self, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "third_party", "node_modules", "graphify-out":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		abs, err := filepath.Abs(filepath.Dir(path))
		if err != nil {
			return err
		}
		if abs == self {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "/proc/loadavg") {
			t.Errorf("%s reads /proc/loadavg; use hoststats.ReadLoadAvg", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
