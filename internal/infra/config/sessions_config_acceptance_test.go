//go:build acceptance

package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Peer notes (docs/specs/peer-notes.md, rev 3): sessions.peer_notes is on when
// unset, off only when written false, and reaches serve through the pushed
// config.

func TestSessionsPeerNotesDefaultsOn(t *testing.T) {
	var c SessionsConfig
	if !c.PeerNotesOn() {
		t.Fatal("unset sessions.peer_notes must be on")
	}
	on, off := true, false
	if !(SessionsConfig{PeerNotes: &on}).PeerNotesOn() {
		t.Error("peer_notes: true read as off")
	}
	if (SessionsConfig{PeerNotes: &off}).PeerNotesOn() {
		t.Error("peer_notes: false read as on")
	}
}

func TestSessionsPeerNotesRoundTrip(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("remotes: []\nsessions:\n  peer_notes: false\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Sessions.PeerNotesOn() {
		t.Fatal("peer_notes: false in the file read as on")
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "peer_notes: false") {
		t.Errorf("marshalled config lost the switch:\n%s", out)
	}
	var plain Config
	if err := yaml.Unmarshal([]byte("remotes: []\n"), &plain); err != nil {
		t.Fatal(err)
	}
	out, _ = yaml.Marshal(plain)
	if strings.Contains(string(out), "sessions:") {
		t.Errorf("an unset sessions block is written out:\n%s", out)
	}
}

func TestServeConfigCarriesSessions(t *testing.T) {
	off := false
	cfg := &Config{Remotes: []Remote{{Name: "devbox", Host: "h", User: "u", Root: "/r"}}, Sessions: SessionsConfig{PeerNotes: &off}}
	data, err := cfg.MarshalServeConfig("h")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "peer_notes: false") {
		t.Fatalf("serve config lacks the switch:\n%s", data)
	}
	var sc ServeConfig
	if err := yaml.Unmarshal(data, &sc); err != nil {
		t.Fatal(err)
	}
	if sc.Sessions.PeerNotesOn() {
		t.Fatal("ServeConfig.Sessions did not carry peer_notes: false")
	}
	on, _ := (&Config{}).MarshalServeConfig("h")
	var def ServeConfig
	if err := yaml.Unmarshal(on, &def); err != nil {
		t.Fatal(err)
	}
	if !def.Sessions.PeerNotesOn() {
		t.Fatal("a config without a sessions block pushes peer notes off")
	}
}
