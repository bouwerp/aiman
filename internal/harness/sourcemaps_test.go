package harness

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// writeTree writes files (relative path -> body) under a new directory.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, p, body)
	}
	return dir
}

// appServer serves path -> body as an app would; a missing path is 404.
func appServer(t *testing.T, pages map[string]string, headers map[string]http.Header) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		for k, v := range headers[r.URL.Path] {
			w.Header()[k] = v
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case strings.HasSuffix(r.URL.Path, ".map"):
			w.Header().Set("Content-Type", "application/json")
		default:
			w.Header().Set("Content-Type", "text/javascript")
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

const builtIndex = `<!doctype html><html><head>
<script type="module" crossorigin src="/assets/index-Bq1.js"></script>
<link rel="modulepreload" crossorigin href="/assets/vendor-X9.js">
</head><body><div id="root"></div></body></html>`

func TestSourcemapsDetected(t *testing.T) {
	ctx := context.Background()
	lim := DefaultSourcemapLimits()

	t.Run("scan of build output", func(t *testing.T) {
		src := writeTree(t, map[string]string{
			"frontend/dist/index.html":              builtIndex,
			"frontend/dist/assets/index-Bq1.js":     "console.log(1);\n//# sourceMappingURL=index-Bq1.js.map\n",
			"frontend/dist/assets/index-Bq1.js.map": `{"version":3}`,
			"frontend/src/main.tsx":                 "export {}\n",
		})
		got := ScanSourcemaps(ctx, src, lim)
		if got.State != SourcemapsPresent || got.Method != SourcemapMethodScan {
			t.Fatalf("scan = %+v, want present by scan", got)
		}
		if len(got.Files) != 1 || got.Files[0] != "frontend/dist/assets/index-Bq1.js" {
			t.Fatalf("files = %q", got.Files)
		}
	})

	t.Run("built page over http", func(t *testing.T) {
		srv, _ := appServer(t, map[string]string{
			"/":                        builtIndex,
			"/assets/index-Bq1.js":     "console.log(1);\n//# sourceMappingURL=index-Bq1.js.map",
			"/assets/index-Bq1.js.map": `{"version":3}`,
			"/assets/vendor-X9.js":     "var a;\n",
		}, nil)
		got := ProbeSourcemaps(ctx, srv.Client(), srv.URL, lim)
		if got.State != SourcemapsPresent || got.Method != SourcemapMethodHTTP {
			t.Fatalf("probe = %+v, want present over http", got)
		}
		if len(got.Files) == 0 || got.Files[0] != "/assets/index-Bq1.js" {
			t.Fatalf("files = %q", got.Files)
		}
	})

	t.Run("vite dev server inline maps", func(t *testing.T) {
		srv, _ := appServer(t, map[string]string{
			"/": `<html><head><script type="module" src="/@vite/client"></script>
<script type="module" src="/src/main.tsx"></script></head></html>`,
			"/@vite/client": "export {};\n",
			"/src/main.tsx": "import x from './x';\n//# sourceMappingURL=data:application/json;base64,eyJ2ZXJzaW9uIjozfQ==\n",
		}, nil)
		got := ProbeSourcemaps(ctx, srv.Client(), srv.URL, lim)
		if got.State != SourcemapsPresent {
			t.Fatalf("probe = %+v, want present", got)
		}
	})

	t.Run("SourceMap header", func(t *testing.T) {
		srv, _ := appServer(t, map[string]string{
			"/":                        builtIndex,
			"/assets/index-Bq1.js":     "console.log(1);\n",
			"/assets/index-Bq1.js.map": `{"version":3}`,
		}, map[string]http.Header{"/assets/index-Bq1.js": {"Sourcemap": {"/assets/index-Bq1.js.map"}}})
		got := ProbeSourcemaps(ctx, srv.Client(), srv.URL, lim)
		if got.State != SourcemapsPresent {
			t.Fatalf("probe = %+v, want present", got)
		}
	})

	t.Run("detect prefers the served page", func(t *testing.T) {
		srv, _ := appServer(t, map[string]string{
			"/":                        builtIndex,
			"/assets/index-Bq1.js":     "x\n//# sourceMappingURL=index-Bq1.js.map\n",
			"/assets/index-Bq1.js.map": `{}`,
		}, nil)
		// The tree says absent; the page the browser gets says present.
		src := writeTree(t, map[string]string{"dist/a.js": "x\n"})
		got := DetectSourcemaps(ctx, src, []SourcemapTarget{{Port: "api", URL: "http://127.0.0.1:1"}, {Port: "web", URL: srv.URL}}, lim)
		if got.State != SourcemapsPresent || got.Port != "web" {
			t.Fatalf("detect = %+v, want present on web", got)
		}
	})
}

func TestNoSourcemapsWarns(t *testing.T) {
	ctx := context.Background()
	lim := DefaultSourcemapLimits()

	t.Run("scan without maps", func(t *testing.T) {
		src := writeTree(t, map[string]string{"dist/assets/index.js": "console.log(1);\n"})
		got := ScanSourcemaps(ctx, src, lim)
		if got.State != SourcemapsAbsent || got.Hidden {
			t.Fatalf("scan = %+v, want absent", got)
		}
	})

	t.Run("hidden maps", func(t *testing.T) {
		src := writeTree(t, map[string]string{
			"build/static/main.js":     "console.log(1);\n",
			"build/static/main.js.map": "{}",
		})
		got := ScanSourcemaps(ctx, src, lim)
		if got.State != SourcemapsAbsent || !got.Hidden {
			t.Fatalf("scan = %+v, want absent and hidden", got)
		}
	})

	t.Run("served bundle without a comment", func(t *testing.T) {
		srv, _ := appServer(t, map[string]string{
			"/":                    builtIndex,
			"/assets/index-Bq1.js": "console.log(1);\n",
			"/assets/vendor-X9.js": "var a;\n",
		}, nil)
		got := ProbeSourcemaps(ctx, srv.Client(), srv.URL, lim)
		if got.State != SourcemapsAbsent {
			t.Fatalf("probe = %+v, want absent", got)
		}
	})

	t.Run("comment names a deleted map", func(t *testing.T) {
		srv, _ := appServer(t, map[string]string{
			"/":                    builtIndex,
			"/assets/index-Bq1.js": "x\n//# sourceMappingURL=index-Bq1.js.map\n",
			"/assets/vendor-X9.js": "var a;\n",
		}, nil)
		got := ProbeSourcemaps(ctx, srv.Client(), srv.URL, lim)
		if got.State != SourcemapsAbsent || !strings.Contains(got.Detail, "404") {
			t.Fatalf("probe = %+v, want absent naming the 404", got)
		}
	})

	t.Run("detect falls back to the tree", func(t *testing.T) {
		src := writeTree(t, map[string]string{"dist/a.js": "x\n"})
		got := DetectSourcemaps(ctx, src, []SourcemapTarget{{Port: "web", URL: "http://127.0.0.1:1"}}, lim)
		if got.State != SourcemapsAbsent || got.Method != SourcemapMethodScan {
			t.Fatalf("detect = %+v, want absent by scan", got)
		}
	})
}

func TestSourcemapsUnknown(t *testing.T) {
	ctx := context.Background()
	lim := DefaultSourcemapLimits()
	src := writeTree(t, map[string]string{"src/main.ts": "x\n", "node_modules/pkg/dist/a.js": "x\n"})
	if got := ScanSourcemaps(ctx, src, lim); got.State != SourcemapsUnknown {
		t.Fatalf("a tree with no build output = %+v, want unknown", got)
	}
	srv, _ := appServer(t, map[string]string{"/": `{"ok":true}`}, nil)
	if got := ProbeSourcemaps(ctx, srv.Client(), srv.URL, lim); got.State != SourcemapsUnknown {
		t.Fatalf("a page with no scripts = %+v, want unknown", got)
	}
	if got := DetectSourcemaps(ctx, src, nil, lim); got.State != SourcemapsUnknown || got.Detail == "" {
		t.Fatalf("detect = %+v, want unknown with a reason", got)
	}
}

func TestSourcemapsScanStopsAtFileLimit(t *testing.T) {
	files := map[string]string{}
	for i := range 30 {
		files["dist/chunk"+strings.Repeat("a", i+1)+".js"] = "x\n"
	}
	src := writeTree(t, files)
	lim := DefaultSourcemapLimits()
	lim.MaxFiles = 10
	got := ScanSourcemaps(context.Background(), src, lim)
	if got.State != SourcemapsUnknown || !strings.Contains(got.Detail, "10 files") {
		t.Fatalf("scan = %+v, want unknown after 10 files", got)
	}
}

func TestSourcemapsScanReadsOnlyTheTail(t *testing.T) {
	lim := DefaultSourcemapLimits()
	lim.TailBytes = 64
	// An inline map longer than the window: the comment start is out of
	// reach and the last line has no start, so the scan cannot tell.
	long := "x\n//# sourceMappingURL=data:application/json;base64," + strings.Repeat("A", 500)
	src := writeTree(t, map[string]string{"dist/a.js": long})
	if got := ScanSourcemaps(context.Background(), src, lim); got.State != SourcemapsUnknown {
		t.Fatalf("scan = %+v, want unknown", got)
	}
	// A short comment inside the window is still found.
	src = writeTree(t, map[string]string{"dist/a.js": strings.Repeat("y", 500) + "\n//# sourceMappingURL=a.js.map\n"})
	if got := ScanSourcemaps(context.Background(), src, lim); got.State != SourcemapsPresent {
		t.Fatalf("scan = %+v, want present", got)
	}
}

func TestSourcemapsScanSkipsSymlinks(t *testing.T) {
	outside := writeTree(t, map[string]string{"secret.js": "x\n//# sourceMappingURL=s.map\n"})
	src := writeTree(t, map[string]string{"dist/a.js": "x\n"})
	if err := os.Symlink(filepath.Join(outside, "secret.js"), filepath.Join(src, "dist", "b.js")); err != nil {
		t.Skip("no symlinks:", err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "dist", "sub")); err != nil {
		t.Fatal(err)
	}
	got := ScanSourcemaps(context.Background(), src, DefaultSourcemapLimits())
	if got.State != SourcemapsAbsent {
		t.Fatalf("scan = %+v: a symlink out of the tree was followed", got)
	}
}

func TestSourcemapsProbeSameOriginOnly(t *testing.T) {
	other, otherHits := appServer(t, map[string]string{"/x.js": "x\n//# sourceMappingURL=x.js.map\n", "/x.js.map": "{}"}, nil)
	srv, _ := appServer(t, map[string]string{
		"/": `<script src="` + other.URL + `/x.js"></script><script src="//evil.example/y.js"></script>`,
	}, nil)
	got := ProbeSourcemaps(context.Background(), srv.Client(), srv.URL, DefaultSourcemapLimits())
	if otherHits.Load() != 0 {
		t.Fatalf("the probe fetched another origin %d times", otherHits.Load())
	}
	if got.State != SourcemapsUnknown {
		t.Fatalf("probe = %+v, want unknown", got)
	}
}

func TestSourcemapsProbeIgnoresRedirects(t *testing.T) {
	other, otherHits := appServer(t, map[string]string{"/": builtIndex}, nil)
	srv := httptest.NewServer(http.RedirectHandler(other.URL+"/", http.StatusFound))
	t.Cleanup(srv.Close)
	got := ProbeSourcemaps(context.Background(), srv.Client(), srv.URL, DefaultSourcemapLimits())
	if otherHits.Load() != 0 || got.State != SourcemapsUnknown {
		t.Fatalf("probe = %+v after %d hits elsewhere, want unknown and none", got, otherHits.Load())
	}
}

func TestSourcemapsProbeBounds(t *testing.T) {
	lim := DefaultSourcemapLimits()
	lim.MaxBody = 256
	lim.MaxScripts = 2
	var scripts strings.Builder
	pages := map[string]string{}
	for i := range 5 {
		p := "/s" + strings.Repeat("x", i) + ".js"
		scripts.WriteString(`<script src="` + p + `"></script>`)
		pages[p] = "x\n"
	}
	pages["/"] = scripts.String()
	pages["/big.js"] = strings.Repeat("z", 1024)
	srv, hits := appServer(t, pages, nil)
	got := ProbeSourcemaps(context.Background(), srv.Client(), srv.URL, lim)
	if n := hits.Load(); n != 3 {
		t.Fatalf("%d requests, want the page and 2 scripts", n)
	}
	if got.State != SourcemapsAbsent {
		t.Fatalf("probe = %+v", got)
	}
	// A bundle over the body cap whose end was never read is undecided.
	pages["/"] = `<script src="/big.js"></script>`
	srv2, _ := appServer(t, pages, nil)
	if got := ProbeSourcemaps(context.Background(), srv2.Client(), srv2.URL, lim); got.State != SourcemapsUnknown {
		t.Fatalf("probe of an oversized bundle = %+v, want unknown", got)
	}
}

func TestSourcemapsProbeTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(block); srv.Close() })
	lim := DefaultSourcemapLimits()
	lim.Timeout = 200 * time.Millisecond
	start := time.Now()
	got := DetectSourcemaps(context.Background(), t.TempDir(), []SourcemapTarget{{Port: "web", URL: srv.URL}}, lim)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("detection took %s", d)
	}
	if got.State != SourcemapsUnknown {
		t.Fatalf("detect = %+v", got)
	}
}

func TestSourcemapsStoredOnRun(t *testing.T) {
	var gotSrc string
	var gotTargets []SourcemapTarget
	tm := newTestManager(t, func(o *Options) {
		o.Sourcemaps = func(_ context.Context, src string, targets []SourcemapTarget) BrowserSourcemaps {
			gotSrc, gotTargets = src, targets
			return BrowserSourcemaps{State: SourcemapsPresent, Method: SourcemapMethodHTTP, Port: "web"}
		}
	})
	repo := testRepo(t, testHarnessFile)
	run := tm.upReady(t, repo, "plain")
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, err := tm.Get(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.BrowserSourcemaps != nil {
			if r.BrowserSourcemaps.State != SourcemapsPresent {
				t.Fatalf("stored %+v", r.BrowserSourcemaps)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run never got browser_sourcemaps")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if gotSrc != filepath.Join(tm.dir, "s1", run.ID, "src") {
		t.Fatalf("src = %s", gotSrc)
	}
	if len(gotTargets) != 1 || gotTargets[0].Port != "web" || gotTargets[0].URL != "http://127.0.0.1:41873" {
		t.Fatalf("targets = %+v", gotTargets)
	}
	stored, err := readRun(filepath.Join(tm.dir, "s1", run.ID, "run.json"))
	if err != nil || stored.BrowserSourcemaps == nil {
		t.Fatalf("run.json lacks browser_sourcemaps: %v", err)
	}
}
