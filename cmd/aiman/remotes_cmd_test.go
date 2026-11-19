package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

var remotesCfg = &config.Config{
	ActiveRemote: "regent0",
	Remotes: []config.Remote{
		{Name: "regent0", User: "code", Host: "regent0", Root: "/home/code/repos"},
		{Name: "box", Host: "box.lan"},
	},
}

func TestRemotesTable(t *testing.T) {
	var b bytes.Buffer
	if err := writeRemotes(&b, remotesCfg, false); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "NAME") {
		t.Fatalf("table:\n%s", b.String())
	}
	if f := strings.Fields(lines[1]); strings.Join(f, " ") != "regent0 code regent0 /home/code/repos yes" {
		t.Fatalf("row %q", lines[1])
	}
	if f := strings.Fields(lines[2]); strings.Join(f, " ") != "box - box.lan -" {
		t.Fatalf("row %q", lines[2])
	}
	b.Reset()
	if err := writeRemotes(&b, &config.Config{}, false); err != nil || !strings.Contains(b.String(), "no remotes") {
		t.Fatalf("empty: %q %v", b.String(), err)
	}
}

func TestRemotesJSON(t *testing.T) {
	var b bytes.Buffer
	if err := writeRemotes(&b, remotesCfg, true); err != nil {
		t.Fatal(err)
	}
	var got struct {
		ActiveRemote string      `json:"active_remote"`
		Remotes      []remoteRow `json:"remotes"`
	}
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := remoteRow{Name: "regent0", User: "code", Host: "regent0", Root: "/home/code/repos", Target: "code@regent0", Active: true}
	if got.ActiveRemote != "regent0" || len(got.Remotes) != 2 || got.Remotes[0] != want || got.Remotes[1].Active {
		t.Fatalf("json %+v", got)
	}
}

// TestConsoleRemoteMissAndUserAtHost is the case that led here: the TUI shows
// code@regent0, and --remote code@regent0 now finds it; a miss names the
// remotes.
func TestConsoleRemoteMissAndUserAtHost(t *testing.T) {
	if r, err := pickConsoleRemote(remotesCfg.Remotes, "", "code@regent0"); err != nil || r.Name != "regent0" {
		t.Fatalf("code@regent0 = %+v %v", r, err)
	}
	_, err := pickConsoleRemote(remotesCfg.Remotes, "", "nope")
	if err == nil || err.Error() != "no remote nope in the config; remotes: regent0, box" {
		t.Fatalf("miss = %v", err)
	}
	if _, err := pickConsoleRemote(remotesCfg.Remotes, "", ""); err == nil || !strings.Contains(err.Error(), "remotes: regent0, box") {
		t.Fatalf("no name with two remotes = %v", err)
	}
}
