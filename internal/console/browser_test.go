package console

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// sourcemapRPC answers a ready run whose browser_sourcemaps is maps (raw
// JSON; empty leaves the field out).
func sourcemapRPC(maps string) *fakeRPC {
	return &fakeRPC{answer: func(method string, _ map[string]any) (json.RawMessage, error) {
		if method != "harness.status" {
			return json.RawMessage(`{}`), nil
		}
		raw := string(runJSON("ready"))
		if maps != "" {
			raw = strings.Replace(raw, `"checks":[]`, `"checks":[],"browser_sourcemaps":`+maps, 1)
		}
		return json.RawMessage(raw), nil
	}}
}

func browserState(t *testing.T, maps string) *browserView {
	t.Helper()
	s, host := newTestServer(t, sourcemapRPC(maps), &fakeForwarder{}, nil)
	s.refresh(context.Background())
	w := do(t, s, "GET", host, "/api/state", testToken, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("state: %d %s", w.Code, w.Body)
	}
	var v struct {
		Browser *browserView `json:"browser"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v.Browser
}

func TestSourcemapsDetected(t *testing.T) {
	b := browserState(t, `{"state":"present","method":"http","port":"web","files":["/assets/index-Bq1.js"]}`)
	if b == nil {
		t.Fatal("no browser section for a ready run with a web port")
	}
	if b.Sourcemaps != "present" || b.Warning != "" || len(b.Fixes) != 0 {
		t.Fatalf("browser = %+v, want present with no warning", b)
	}
	if b.URL != "http://127.0.0.1:52000" || b.Port != "web" {
		t.Fatalf("url = %q port = %q, want the web forward", b.URL, b.Port)
	}

	// The tab and its script are on the page.
	s, host := newTestServer(t, readyRPC(), &fakeForwarder{}, nil)
	page := do(t, s, "GET", host, "/", "", "", "").Body.String()
	for _, want := range []string{`id="dbg-browser"`, `src="/debug-browser.js"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if w := do(t, s, "GET", host, "/debug-browser.js", "", "", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "aimanConsole") {
		t.Fatalf("debug-browser.js: %d", w.Code)
	}
}

func TestNoSourcemapsWarns(t *testing.T) {
	b := browserState(t, `{"state":"absent","method":"scan","detail":"none of the 3 JavaScript files in the build output names a source map"}`)
	if b == nil || b.Sourcemaps != "absent" || b.Warning == "" {
		t.Fatalf("browser = %+v, want a warning", b)
	}
	if !containsAny(b.Fixes, "build.sourcemap: true") || !containsAny(b.Fixes, "deletes") {
		t.Fatalf("fixes = %q, want the Vite setting and the deleting plugin", b.Fixes)
	}
	if !strings.Contains(b.Warning, "none of the 3") {
		t.Fatalf("warning = %q, want serve's detail", b.Warning)
	}

	hidden := browserState(t, `{"state":"absent","method":"scan","hidden":true}`)
	if hidden == nil || !containsAny(hidden.Fixes, "'hidden'") {
		t.Fatalf("hidden maps: %+v", hidden)
	}

	unknown := browserState(t, `{"state":"unknown","detail":"GET / answered 404"}`)
	if unknown == nil || unknown.Warning != "" || !strings.Contains(unknown.Note, "GET / answered 404") {
		t.Fatalf("unknown: %+v, want a note and no warning", unknown)
	}

	checking := browserState(t, "")
	if checking == nil || checking.Sourcemaps != "checking" || checking.Warning != "" {
		t.Fatalf("not yet checked: %+v", checking)
	}
}

func containsAny(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
