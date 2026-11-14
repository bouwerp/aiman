//go:build pileup_g

package server

// Contract:
//   - var dumpPressure func() (some, full float64, ok bool) is the user
//     slice's memory pressure avg10 the auto dump reads (the gate's reading);
//     ok false is no reading. Tests swap it.
//   - var dumpCensus func() hoststats.AimanProcs is the aiman process census
//     (serve and the pty holders left out) WriteDump includes. Tests swap it.
//   - func (s *Server) autoDumpTick(d *autoDump, now time.Time, dir string)
//     is one auto-dump check, which runAutoDump runs every autoDumpCheck: it
//     calls d.due(now, atCap || full >= 50) with in flight at the cap and the
//     `full` avg10 from dumpPressure (`some` never counts), and writes a dump
//     into dir when due.
//   - WriteDump writes the census as a line holding AimanProcs.Summary().

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/hoststats"
)

// withPressure points dumpPressure at *some/*full for the test.
func withPressure(t *testing.T, some, full *float64, ok *bool) {
	t.Helper()
	old := dumpPressure
	dumpPressure = func() (float64, float64, bool) { return *some, *full, *ok }
	t.Cleanup(func() { dumpPressure = old })
}

func dumpCount(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if dumpName.MatchString(e.Name()) {
			n++
		}
	}
	return n
}

// tickEvery runs autoDumpTick every autoDumpCheck over [from, to] with the
// pressure set by set(at), and returns the offsets at which a dump appeared.
func tickEvery(t *testing.T, srv *Server, d *autoDump, dir string, t0 time.Time, from, to time.Duration, set func(at time.Duration)) []time.Duration {
	t.Helper()
	var fired []time.Duration
	for at := from; at <= to; at += autoDumpCheck {
		set(at)
		before := dumpCount(t, dir)
		srv.autoDumpTick(d, t0.Add(at), dir)
		if dumpCount(t, dir) > before {
			fired = append(fired, at)
		}
	}
	return fired
}

var pressureT0 = time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)

func TestAcceptanceAutoDumpFiresAfter30sOfFullPressure(t *testing.T) {
	if autoDumpCheck != 2*time.Second {
		t.Fatalf("autoDumpCheck = %v, want 2s", autoDumpCheck)
	}
	srv, _, _ := ptyServer(t)
	dir := t.TempDir()
	some, full, ok := 60.0, 50.0, true
	withPressure(t, &some, &full, &ok)
	var d autoDump
	fired := tickEvery(t, srv, &d, dir, pressureT0, 0, 40*time.Second, func(time.Duration) {})
	if len(fired) != 1 || fired[0] != 30*time.Second {
		t.Fatalf("dumps at %v, want exactly one at 30s of full avg10 >= 50", fired)
	}
}

func TestAcceptanceAutoDumpIgnoresPressureBelowTheLine(t *testing.T) {
	cases := []struct {
		name       string
		some, full float64
		ok         bool
	}{
		{"full just under 50", 90, 49.9, true},
		{"some high, full low", 95, 10, true},
		{"no reading", 99, 99, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _, _ := ptyServer(t)
			dir := t.TempDir()
			some, full, ok := c.some, c.full, c.ok
			withPressure(t, &some, &full, &ok)
			var d autoDump
			if fired := tickEvery(t, srv, &d, dir, pressureT0, 0, 2*time.Minute, func(time.Duration) {}); len(fired) != 0 {
				t.Fatalf("dumps at %v, want none", fired)
			}
		})
	}
}

func TestAcceptanceAutoDumpPressureDipRestartsTheClock(t *testing.T) {
	srv, _, _ := ptyServer(t)
	dir := t.TempDir()
	some, full, ok := 0.0, 0.0, true
	withPressure(t, &some, &full, &ok)
	var d autoDump
	// High from 0 to 20s, low at 22s, high again from 24s: due 30s after 24s.
	fired := tickEvery(t, srv, &d, dir, pressureT0, 0, 60*time.Second, func(at time.Duration) {
		full = 70
		if at == 22*time.Second {
			full = 5
		}
	})
	if len(fired) != 1 || fired[0] != 54*time.Second {
		t.Fatalf("dumps at %v, want one at 54s (30s after pressure came back)", fired)
	}
}

func TestAcceptanceAutoDumpPressureRespectsSpacing(t *testing.T) {
	srv, _, _ := ptyServer(t)
	dir := t.TempDir()
	some, full, ok := 80.0, 80.0, true
	withPressure(t, &some, &full, &ok)
	var d autoDump
	end := 30*time.Second + autoDumpEvery
	fired := tickEvery(t, srv, &d, dir, pressureT0, 0, end, func(time.Duration) {})
	want := []time.Duration{30 * time.Second, end}
	if len(fired) != 2 || fired[0] != want[0] || fired[1] != want[1] {
		t.Fatalf("dumps at %v under sustained pressure, want %v (10 minutes apart)", fired, want)
	}
}

func TestAcceptanceDumpHoldsTheCensus(t *testing.T) {
	census := hoststats.AimanProcs{
		Total:   169,
		ByState: map[string]int{"D": 52, "S": 117},
		Parents: []hoststats.ProcParent{{PID: 3834565, Name: "sshd-session", Count: 26}},
	}
	old := dumpCensus
	dumpCensus = func() hoststats.AimanProcs { return census }
	t.Cleanup(func() { dumpCensus = old })

	srv, _, _ := ptyServer(t)
	path, err := srv.WriteDump(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, census.Summary()) {
		t.Fatalf("dump lacks the census line %q:\n%s", census.Summary(), body[:min(len(body), 2000)])
	}
	if goroutines := strings.Index(body, "goroutines:"); goroutines >= 0 && strings.Index(body, census.Summary()) > goroutines {
		t.Error("census written inside the goroutine stacks, want it in the header before them")
	}
}

func TestAcceptanceAutoDumpFromPressureHoldsTheCensus(t *testing.T) {
	census := hoststats.AimanProcs{
		Total:   40,
		ByState: map[string]int{"D": 40},
		Parents: []hoststats.ProcParent{{PID: 77, Name: "sshd-session", Count: 40}},
	}
	old := dumpCensus
	dumpCensus = func() hoststats.AimanProcs { return census }
	t.Cleanup(func() { dumpCensus = old })

	srv, _, _ := ptyServer(t)
	dir := t.TempDir()
	some, full, ok := 50.0, 50.0, true
	withPressure(t, &some, &full, &ok)
	var d autoDump
	if fired := tickEvery(t, srv, &d, dir, pressureT0, 0, 30*time.Second, func(time.Duration) {}); len(fired) != 1 {
		t.Fatalf("dumps at %v, want one", fired)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !dumpName.MatchString(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), census.Summary()) {
			t.Fatalf("pressure dump lacks the census line %q", census.Summary())
		}
		return
	}
	t.Fatal("no dump file found")
}
