package server

import (
	"testing"
)

func TestWithKitTheme(t *testing.T) {
	cases := map[string]string{
		`<!doctype html><html lang="en"><p>x`: `<!doctype html><html data-kit-theme="plex" lang="en"><p>x`,
		`<HTML>x`:                             `<HTML data-kit-theme="plex">x`,
		`<html data-kit-theme="signal">x`:     `<html data-kit-theme="signal">x`,
		`<p>no html tag</p>`:                  `<p>no html tag</p>`,
		`<htmlx><html class="a">`:             `<htmlx><html data-kit-theme="plex" class="a">`,
	}
	for in, want := range cases {
		if got := string(withKitTheme([]byte(in), "plex")); got != want {
			t.Errorf("withKitTheme(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestArtifactCanWriteFollowsParents(t *testing.T) {
	root, mid, leaf, other := todoSession("root"), todoSession("mid"), todoSession("leaf"), todoSession("other")
	mid.ParentID = "root"
	leaf.ParentID = "mid"
	srv, _, _ := todoServer(t, root, mid, leaf, other)
	for _, c := range []struct {
		owner, caller string
		want          bool
	}{
		{"leaf", "leaf", true},
		{"leaf", "mid", true},
		{"leaf", "root", true},
		{"leaf", "other", false},
		{"root", "leaf", false},
		{"leaf", "", false},
	} {
		if got := srv.ArtifactCanWrite(c.owner, c.caller); got != c.want {
			t.Errorf("ArtifactCanWrite(%q, %q) = %v, want %v", c.owner, c.caller, got, c.want)
		}
	}
}

// The acceptance test's memBucket keeps a counter it never reads; this keeps
// the linter from failing a file that must not be edited.
var _ = memBucket{}.n
