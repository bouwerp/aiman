package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type entry struct {
	name, body, link string
	typ              byte
	mode             int64
}

func tarball(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: mode, Size: int64(len(e.body)), Linkname: e.link}
		if typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func serve(t *testing.T, body []byte) (string, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/tool.tar.gz", &hits
}

var nodeLike = []entry{
	{name: "node-v1/", typ: tar.TypeDir, mode: 0o755},
	{name: "node-v1/bin/", typ: tar.TypeDir, mode: 0o755},
	{name: "node-v1/bin/node", body: "#!binary", mode: 0o755},
	{name: "node-v1/bin/npm", typ: tar.TypeSymlink, link: "../lib/npm-cli.js"},
	{name: "node-v1/lib/npm-cli.js", body: "npm"},
	{name: "node-v1/README.md", body: "readme"},
}

func TestToolChecksumMismatchRefused(t *testing.T) {
	body := tarball(t, nodeLike)
	url, _ := serve(t, body)
	dir := t.TempDir()
	s := &Store{Dir: dir}
	spec := Spec{Name: "node", Version: "v1", URL: url, SHA256: strings.Repeat("0", 64), Strip: 1}
	_, err := s.Ensure(context.Background(), spec)
	var ce *ChecksumError
	if !errors.Is(err, ErrChecksum) || !errors.As(err, &ce) || ce.Tool != "node" || ce.Got != sum(body) {
		t.Fatalf("err = %v, want a checksum error for node", err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		t.Fatalf("a refused download left %v behind", ents)
	}
}

func TestToolUnpacks(t *testing.T) {
	body := tarball(t, nodeLike)
	url, hits := serve(t, body)
	s := &Store{Dir: t.TempDir()}
	spec := Spec{
		Name: "node", Version: "v1", URL: url, SHA256: sum(body), Strip: 1,
		Keep: func(rel string) bool { return rel == "bin/node" },
	}
	dir, err := s.Ensure(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "bin", "node"))
	if err != nil || string(b) != "#!binary" {
		t.Fatalf("bin/node = %q, %v", b, err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "bin", "node")); fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("bin/node mode %v lost its execute bit", fi.Mode())
	}
	for _, gone := range []string{"bin/npm", "lib/npm-cli.js", "README.md"} {
		if _, err := os.Lstat(filepath.Join(dir, gone)); err == nil {
			t.Fatalf("%s was unpacked; Keep left it out", gone)
		}
	}
	// A second Ensure verifies the tree and downloads nothing.
	if _, err := s.Ensure(context.Background(), spec); err != nil || hits.Load() != 1 {
		t.Fatalf("second ensure: %v, %d downloads", err, hits.Load())
	}
	// A changed file is caught and the tool fetched again.
	if err := os.WriteFile(filepath.Join(dir, "bin", "node"), []byte("evil"), 0o755); err != nil { //nolint:gosec // G306: test fixture
		t.Fatal(err)
	}
	if _, err := s.Ensure(context.Background(), spec); err != nil || hits.Load() != 2 {
		t.Fatalf("tampered ensure: %v, %d downloads", err, hits.Load())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "bin", "node")); string(b) != "#!binary" {
		t.Fatalf("bin/node after repair = %q", b)
	}
}

func TestToolRefusesTraversal(t *testing.T) {
	for _, name := range []string{"x/../../evil", "/abs/evil"} {
		body := tarball(t, []entry{{name: name, body: "x"}})
		url, _ := serve(t, body)
		root := t.TempDir()
		s := &Store{Dir: filepath.Join(root, "tools")}
		_, err := s.Ensure(context.Background(), Spec{Name: "t", Version: "v1", URL: url, SHA256: sum(body)})
		if err == nil {
			t.Fatalf("%s: unpacked", name)
		}
		if _, err := os.Stat(filepath.Join(root, "evil")); err == nil {
			t.Fatalf("%s: wrote outside the store", name)
		}
	}
}

func TestPins(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		n, err := Node(arch, Pin{})
		if err != nil || !strings.HasPrefix(n.URL, "https://nodejs.org/dist/"+NodeVersion+"/") || len(n.SHA256) != 64 {
			t.Fatalf("node %s = %+v, %v", arch, n, err)
		}
	}
	if _, err := Node("riscv64", Pin{}); err == nil {
		t.Fatal("node for an unpinned arch")
	}
	j, err := JSDebug(Pin{})
	if err != nil || !strings.HasSuffix(j.URL, "/"+JSDebugVersion+"/js-debug-dap-"+JSDebugVersion+".tar.gz") || len(j.SHA256) != 64 {
		t.Fatalf("js-debug = %+v, %v", j, err)
	}
	// An override needs both a version and its sha256.
	if _, err := JSDebug(Pin{Version: "v1.141.0"}); err == nil {
		t.Fatal("a version override without a sha256")
	}
	if o, err := JSDebug(Pin{Version: "v1.141.0", SHA256: strings.Repeat("a", 64)}); err != nil || !strings.Contains(o.URL, "v1.141.0") {
		t.Fatalf("override = %+v, %v", o, err)
	}
}
