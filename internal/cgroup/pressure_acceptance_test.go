//go:build pileup_d

package cgroup

// Contract (docs/specs/client-pileup-guard.md, Behaviour P3, Contracts):
//   - type Pressure struct{ SomeAvg10, FullAvg10 float64 }
//   - func ReadPressure(dir string) (Pressure, error) reads <dir>/memory.pressure
//     and returns avg10 of its "some" and "full" lines. A file with no "full"
//     line (older kernels, /proc/pressure/cpu style) is not an error: FullAvg10
//     is 0.

import (
	"os"
	"path/filepath"
	"testing"
)

func writePressure(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "memory.pressure"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAcceptanceReadPressureParsesSomeAndFull(t *testing.T) {
	dir := writePressure(t, "some avg10=96.12 avg60=80.00 avg300=40.10 total=8838764221\n"+
		"full avg10=33.50 avg60=20.25 avg300=9.00 total=7497116825\n")
	got, err := ReadPressure(dir)
	if err != nil {
		t.Fatalf("ReadPressure: %v", err)
	}
	if got.SomeAvg10 != 96.12 || got.FullAvg10 != 33.50 {
		t.Fatalf("ReadPressure = %+v, want some 96.12 full 33.50", got)
	}
}

func TestAcceptanceReadPressureToleratesNoFullLine(t *testing.T) {
	dir := writePressure(t, "some avg10=12.00 avg60=3.00 avg300=1.00 total=42\n")
	got, err := ReadPressure(dir)
	if err != nil {
		t.Fatalf("ReadPressure with no full line: %v", err)
	}
	if got.SomeAvg10 != 12 || got.FullAvg10 != 0 {
		t.Fatalf("ReadPressure = %+v, want some 12 full 0", got)
	}
}
