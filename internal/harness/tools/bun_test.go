package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type zipEntry struct {
	name, body string
	mode       fs.FileMode
}

func zipball(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// bunLike is the shape of bun-linux-<arch>.zip, plus a symlink and a stray
// file that must not be unpacked.
var bunLike = []zipEntry{
	{name: "bun-linux-x64/", mode: fs.ModeDir | 0o755},
	{name: "bun-linux-x64/bun", body: "#!bun", mode: 0o755},
	{name: "bun-linux-x64/bunx", body: "bun", mode: fs.ModeSymlink | 0o777},
	{name: "bun-linux-x64/extra.txt", body: "extra"},
}

func TestToolUnpacksZip(t *testing.T) {
	body := zipball(t, bunLike)
	url, hits := serve(t, body)
	s := &Store{Dir: t.TempDir()}
	spec := Spec{
		Name: "bun", Version: "v1", URL: url, SHA256: sum(body), Strip: 1, Format: FormatZip,
		Keep: func(rel string) bool { return rel == "bun" },
	}
	dir, err := s.Ensure(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "bun"))
	if err != nil || string(b) != "#!bun" {
		t.Fatalf("bun = %q, %v", b, err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "bun")); fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("bun mode %v lost its execute bit", fi.Mode())
	}
	for _, gone := range []string{"bunx", "extra.txt"} {
		if _, err := os.Lstat(filepath.Join(dir, gone)); err == nil {
			t.Fatalf("%s was unpacked", gone)
		}
	}
	if _, err := s.Ensure(context.Background(), spec); err != nil || hits.Load() != 1 {
		t.Fatalf("second ensure: %v, %d downloads", err, hits.Load())
	}
}

func TestToolZipRefusesTraversal(t *testing.T) {
	for _, name := range []string{"x/../../evil", "/abs/evil", `x\..\evil`} {
		body := zipball(t, []zipEntry{{name: name, body: "x"}})
		url, _ := serve(t, body)
		root := t.TempDir()
		s := &Store{Dir: filepath.Join(root, "tools")}
		_, err := s.Ensure(context.Background(), Spec{Name: "t", Version: "v1", URL: url, SHA256: sum(body), Format: FormatZip})
		if err == nil {
			t.Fatalf("%s: unpacked", name)
		}
		if _, err := os.Stat(filepath.Join(root, "evil")); err == nil {
			t.Fatalf("%s: wrote outside the store", name)
		}
	}
}

func TestToolZipChecksumRefused(t *testing.T) {
	body := zipball(t, bunLike)
	url, _ := serve(t, body)
	dir := t.TempDir()
	s := &Store{Dir: dir}
	_, err := s.Ensure(context.Background(), Spec{Name: "bun", Version: "v1", URL: url, SHA256: strings.Repeat("0", 64), Format: FormatZip, Hint: "build it yourself"})
	var ce *ChecksumError
	if !errors.As(err, &ce) || ce.Tool != "bun" || !strings.Contains(err.Error(), "build it yourself") {
		t.Fatalf("err = %v, want a checksum error with the hint", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("a refused download left %v behind", ents)
	}
}

func TestToolSingleFileFromLocalPath(t *testing.T) {
	src := filepath.Join(t.TempDir(), "bun-dap-v1.3.14.js")
	body := []byte("// bun-dap\n")
	if err := os.WriteFile(src, body, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{Dir: t.TempDir()}
	dir, err := s.Ensure(context.Background(), Spec{Name: "bun-dap", Version: "v1", URL: src, SHA256: sum(body), Format: FormatFile, File: BunDAPFile})
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, BunDAPFile)); err != nil || !bytes.Equal(b, body) {
		t.Fatalf("%s = %q, %v", BunDAPFile, b, err)
	}
	for _, bad := range []string{"", "../x.js", "tool.json", ".hidden"} {
		if _, err := s.Ensure(context.Background(), Spec{Name: "bun-dap", Version: "v1", URL: src, SHA256: sum(body), Format: FormatFile, File: bad}); err == nil {
			t.Fatalf("file name %q accepted", bad)
		}
	}
}

func TestBunPins(t *testing.T) {
	for arch, asset := range map[string]string{"amd64": "bun-linux-x64.zip", "arm64": "bun-linux-aarch64.zip"} {
		b, err := Bun(arch, Pin{})
		if err != nil || b.URL != "https://github.com/oven-sh/bun/releases/download/bun-"+BunVersion+"/"+asset || len(b.SHA256) != 64 || b.Format != FormatZip {
			t.Fatalf("bun %s = %+v, %v", arch, b, err)
		}
		if !b.Keep("bun") || b.Keep("LICENSE") {
			t.Fatalf("bun %s keeps the wrong files", arch)
		}
	}
	if _, err := Bun("riscv64", Pin{}); err == nil {
		t.Fatal("bun for an unpinned arch")
	}

	asset := "bun-dap-" + BunVersion + ".js"
	for release, want := range map[string]string{
		"v0.47.0": "https://github.com/bouwerp/aiman/releases/download/v0.47.0/" + asset,
		"dev":     "https://github.com/bouwerp/aiman/releases/latest/download/" + asset,
	} {
		d, err := BunDAP(release, Pin{})
		if err != nil || d.URL != want || d.SHA256 != BunDAPSHA256 || d.Format != FormatFile || d.File != BunDAPFile || d.Hint == "" {
			t.Fatalf("bun-dap for %s = %+v, %v", release, d, err)
		}
	}
	// A local build: a path with its version and sha256.
	local := Pin{Version: "v1.4.4", SHA256: strings.Repeat("b", 64), URL: "/home/u/bun-dap-v1.4.4.js"}
	if d, err := BunDAP("dev", local); err != nil || d.URL != local.URL || d.Version != "v1.4.4" {
		t.Fatalf("local bun-dap = %+v, %v", d, err)
	}
	for _, bad := range []Pin{
		{URL: "/home/u/bun-dap.js"}, // no version or sum
		{Version: "v1.4.4", SHA256: strings.Repeat("b", 64), URL: "http://x/y.js"}, // not https
		{Version: "v1.4.4", SHA256: strings.Repeat("b", 64), URL: "rel/y.js"},      // relative
		{Version: "v1.4.4", SHA256: strings.Repeat("b", 64), URL: "/a/../y.js"},    // not clean
	} {
		if _, err := BunDAP("dev", bad); err == nil {
			t.Fatalf("pin %+v accepted", bad)
		}
	}
}

// TestBunDAPBuildPin keeps build/bun-dap/build.sh on the Bun version the
// pins name: the build refuses any other Bun, so a drift would make every
// release's asset miss BunDAPSHA256.
func TestBunDAPBuildPin(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "build", "bun-dap", "build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "\nBUN_VERSION=" + strings.TrimPrefix(BunVersion, "v") + "\n"; !strings.Contains(string(b), want) {
		t.Fatalf("build.sh does not set %q", strings.TrimSpace(want))
	}
}
