package config

import (
	"strings"
	"testing"
)

var findRemotes = []Remote{
	{Name: "regent0", User: "code", Host: "regent0", Root: "/home/code/repos"},
	{Name: "box", User: "dev", Host: "box.lan"},
	{Name: "box-2", User: "ops", Host: "box.lan"},
}

// TestFindRemoteNameFirst matches a name ignoring case before anything else,
// even when the same text is another remote's host.
func TestFindRemoteNameFirst(t *testing.T) {
	remotes := append([]Remote{{Name: "box.lan", Host: "other"}}, findRemotes...)
	if r, err := FindRemote(remotes, "BOX.lan"); err != nil || r.Host != "other" {
		t.Fatalf("got %+v %v", r, err)
	}
	if r, err := FindRemote(findRemotes, "Regent0"); err != nil || r.Name != "regent0" {
		t.Fatalf("got %+v %v", r, err)
	}
}

// TestFindRemoteUserAtHost takes the TUI's label user@host when one remote
// has it, and refuses one that several remotes share.
func TestFindRemoteUserAtHost(t *testing.T) {
	if r, err := FindRemote(findRemotes, "code@regent0"); err != nil || r.Name != "regent0" {
		t.Fatalf("code@regent0 = %+v %v", r, err)
	}
	if r, err := FindRemote(findRemotes, "ops@box.lan"); err != nil || r.Name != "box-2" {
		t.Fatalf("ops@box.lan = %+v %v", r, err)
	}
	if _, err := FindRemote(findRemotes, "box.lan"); err == nil || !strings.Contains(err.Error(), "box, box-2") {
		t.Fatalf("shared host: %v", err)
	}
}

// TestFindRemoteMissListsNames puts every configured name in the miss.
func TestFindRemoteMissListsNames(t *testing.T) {
	_, err := FindRemote(findRemotes, "root@regent0")
	if err == nil || err.Error() != "no remote root@regent0 in the config; remotes: regent0, box, box-2" {
		t.Fatalf("err = %v", err)
	}
	if _, err := FindRemote(nil, "x"); err == nil || !strings.Contains(err.Error(), "remotes: none") {
		t.Fatalf("empty config: %v", err)
	}
}

func TestRemoteActiveByNameOrHost(t *testing.T) {
	r := findRemotes[1]
	if !r.IsActive("BOX") || !r.IsActive("box.lan") || r.IsActive("") || r.IsActive("regent0") {
		t.Fatal("active matches the name or the host only")
	}
	if r.Target() != "dev@box.lan" || (Remote{Host: "h"}).Target() != "h" {
		t.Fatalf("target %q", r.Target())
	}
}
