package main

import (
	"bufio"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/limits"
)

func TestFormatLimits(t *testing.T) {
	now := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	var b strings.Builder
	err := formatLimits(&b, limits.Snapshot{Readings: []limits.Reading{
		{Provider: "codex", Plan: "plus", State: limits.StateOK, ObservedAt: now.Add(-2 * time.Hour), Windows: []limits.Window{
			{Label: "5h", Reset: true},
			{Label: "week", UsedPercent: 30.5, ResetsText: "November 11, 13:44"},
		}},
		{Provider: "grok", State: limits.StateUnknown, Reason: "screen changed", ObservedAt: now.Add(-5 * time.Minute), Windows: []limits.Window{{Label: "week", UsedPercent: 19}}},
		{Provider: "deepseek", State: limits.StateOK, ObservedAt: now, Balances: []limits.Balance{{Amount: "12.10", Currency: "USD"}}},
		{Provider: "copilot", State: limits.StateUnavailable},
		{Provider: "opencode", State: limits.StateUnavailable, Reason: "needs a login"},
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"5h reset, week 30.5% (resets November 11, 13:44)",
		"2h ago",
		"week 19%, unknown: screen changed",
		"5m ago",
		"12.10 USD",
		"unavailable: needs a login",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "copilot") {
		t.Errorf("a tool not installed must be dropped:\n%s", out)
	}
}

func TestWaitForLimitsPollsUntilSettledWithoutForcingAgain(t *testing.T) {
	var refreshes []bool
	answers := []limits.Snapshot{{Refreshing: true}, {Refreshing: true}, {Readings: []limits.Reading{{Provider: "a"}}}}
	snap, err := waitForLimits(true, func(time.Duration) {}, func(refresh bool) (limits.Snapshot, error) {
		refreshes = append(refreshes, refresh)
		s := answers[0]
		answers = answers[1:]
		return s, nil
	})
	if err != nil || len(snap.Readings) != 1 {
		t.Fatalf("snap %+v err %v", snap, err)
	}
	if len(refreshes) != 3 || !refreshes[0] || refreshes[1] || refreshes[2] {
		t.Fatalf("refresh flags %v: only the first call may force", refreshes)
	}
}

func TestWaitForLimitsStopsAtItsLimitAndOnError(t *testing.T) {
	var slept time.Duration
	snap, err := waitForLimits(false, func(d time.Duration) { slept += d }, func(bool) (limits.Snapshot, error) {
		return limits.Snapshot{Refreshing: true}, nil
	})
	if err != nil || !snap.Refreshing || slept < limitsWaitFor || slept > limitsWaitFor+limitsPollEvery {
		t.Fatalf("snap %+v err %v slept %v", snap, err, slept)
	}
	boom := errors.New("boom")
	if _, err := waitForLimits(false, func(time.Duration) { t.Fatal("slept after an error") }, func(bool) (limits.Snapshot, error) {
		return limits.Snapshot{Refreshing: true}, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
}

func TestLimitsSkipsTheDatabase(t *testing.T) {
	home := t.TempDir()
	_ = runWithArgs(t, home, "limits")
	if _, err := os.Stat(filepath.Join(home, ".aiman", config.DBName)); err == nil {
		t.Fatal("limits opened the database; it only needs the socket")
	}
}

// The CLI decodes what the handler sends: an embedded snapshot beside its type.
func TestCallLimitsDecodesTheResponse(t *testing.T) {
	dir, err := os.MkdirTemp("", "lim")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	params := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		line, _ := bufio.NewReader(conn).ReadBytes('\n')
		params <- string(line)
		_, _ = conn.Write([]byte(`{"id":"1","result":{"type":"limits","refreshing":true,"readings":[{"provider":"codex","state":"ok","plan":"plus"}]}}` + "\n"))
	}()
	snap, err := callLimits(sock, true)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Refreshing || len(snap.Readings) != 1 || snap.Readings[0].Plan != "plus" {
		t.Fatalf("snap %+v", snap)
	}
	if got := <-params; !strings.Contains(got, `"method":"limits.get"`) || !strings.Contains(got, `"refresh":true`) {
		t.Fatalf("request %s", got)
	}
}
