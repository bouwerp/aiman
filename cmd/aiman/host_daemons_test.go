package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/remotesvc"
)

func TestHostDaemonsArgs(t *testing.T) {
	cases := []struct {
		args   []string
		method string
		want   map[string]any
		ok     bool
	}{
		{[]string{"daemons"}, "host.daemons", map[string]any{}, true},
		{[]string{"daemons", "--json"}, "host.daemons", map[string]any{}, true},
		{[]string{"logs", "gateway", "--lines", "5"}, "host.logs", map[string]any{"daemon": "gateway", "lines": 5}, true},
		{[]string{"restart", "serve", "--force"}, "host.restart", map[string]any{"daemon": "serve", "force": true}, true},
		{[]string{"logs"}, "", nil, false},
		{[]string{"logs", "serve", "--force"}, "", nil, false},
		{[]string{"restart", "serve", "gateway"}, "", nil, false},
		{[]string{"logs", "serve", "--lines", "x"}, "", nil, false},
	}
	for _, c := range cases {
		m, p, _, ok := hostDaemonsArgs(c.args)
		if ok != c.ok || m != c.method || (ok && len(p) != len(c.want)) {
			t.Errorf("%v: %q %v %v", c.args, m, p, ok)
			continue
		}
		for k, v := range c.want {
			if p[k] != v {
				t.Errorf("%v: %s = %v, want %v", c.args, k, p[k], v)
			}
		}
	}
}

func TestHostDaemonsReadsCachesAndLogs(t *testing.T) {
	var calls atomic.Int32
	h := newHostDaemons(nil, "v0.47.0", t.TempDir())
	h.run = func(_ context.Context, script string) (string, error) {
		calls.Add(1)
		switch {
		case strings.Contains(script, remotesvc.LogSourceMarker):
			return remotesvc.LogSourceMarker + "journal\none\ntwo\n", nil
		case strings.Contains(script, "aiman-gateway"):
			return "INSTALLED=1\nDRIVER=systemd\nACTIVE=active\nPID=" + itoaPID() + "\nVERSION=aiman-gateway v0.47.0\n", nil
		default:
			return "INSTALLED=0\n", nil
		}
	}
	rows, err := h.Daemons(context.Background(), false)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows %+v err %v", rows, err)
	}
	if s := rows[0]; s.Kind != "serve" || s.PID != os.Getpid() || s.RSSBytes == 0 || s.StartedAt.IsZero() || time.Since(s.StartedAt) < 0 {
		t.Fatalf("serve row %+v", s)
	}
	if g := rows[1]; !g.Installed || g.Driver != "systemd" || g.Version != "v0.47.0" || g.PID != os.Getpid() || g.RSSBytes == 0 {
		t.Fatalf("gateway row %+v", g)
	}
	if rows[2].Installed {
		t.Fatalf("trigger row %+v", rows[2])
	}
	n := calls.Load()
	if _, err := h.Daemons(context.Background(), false); err != nil || calls.Load() != n {
		t.Fatalf("cached read ran scripts: %d -> %d", n, calls.Load())
	}
	if _, err := h.Daemons(context.Background(), true); err != nil || calls.Load() == n {
		t.Fatal("refresh did not read again")
	}
	logs, err := h.Logs(context.Background(), "serve", 2)
	if err != nil || logs.Source != "journal" || !slices.Equal(logs.Lines, []string{"one", "two"}) {
		t.Fatalf("logs %+v err %v", logs, err)
	}
}

func TestHostDaemonsRedactValuesReadsTheGatewayToken(t *testing.T) {
	home := t.TempDir()
	h := newHostDaemons(nil, "v0.47.0", home)
	if v := h.RedactValues(); v != nil {
		t.Fatalf("no token file: %v", v)
	}
	if err := os.MkdirAll(filepath.Join(home, ".aiman"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aiman", gatewayTokenFile), []byte("tok123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v := h.RedactValues(); !slices.Equal(v, []string{"tok123"}) {
		t.Fatalf("values %v", v)
	}
}

func itoaPID() string { return strconv.Itoa(os.Getpid()) }
