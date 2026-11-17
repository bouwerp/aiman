package images_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness/images"
)

// fakeDocker records docker CLI calls and answers success.
type fakeDocker struct {
	mu    sync.Mutex
	calls [][]string
}

func (d *fakeDocker) run(_ context.Context, args []string) (string, int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, args)
	return "sha256:scanner\n", 0, nil
}

func yaraZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("packages/core/yara-rules-core.yar")
	_, _ = w.Write([]byte("rule x { condition: false }"))
	_ = zw.Close()
	return buf.Bytes()
}

func TestMaintainRefreshesAndRescans(t *testing.T) {
	z := yaraZip(t)
	sum := sha256.Sum256(z)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/release":
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "20261115", "assets": []map[string]string{{
				"name": "yara-forge-rules-core.zip", "digest": "sha256:" + hex.EncodeToString(sum[:]),
				"browser_download_url": srv.URL + "/core.zip",
			}}})
		case r.URL.Path == "/core.zip":
			_, _ = w.Write(z)
		case strings.HasSuffix(r.URL.Path, "/all.zip"):
			_, _ = w.Write([]byte("zip"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	f := newVetFixture(t, nil)
	img := f.push("redis:7", "a")
	_, _ = f.ensure(t, "redis:7", "s1", false)
	if _, err := f.v.Approve(context.Background(), images.ApproveRequest{Digest: img.Digest(), By: images.ApprovedByOperator}); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(f.v.SBOMPath(img.Digest()), []byte("{}"), 0o600)

	docker := &fakeDocker{}
	dbDir := filepath.Join(f.dir, "db")
	m := &images.Maintainer{Vetter: f.v, Every: 24 * time.Hour, Refresher: images.Refresher{
		DBDir: dbDir, Image: "sha256:scanner", Docker: docker.run, OSVBase: srv.URL, OSVEcosystems: []string{"npm"},
		YARARelease: srv.URL + "/release",
	}}
	m.Step(context.Background())
	meta, err := images.ReadDBMeta(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := meta.CheckFresh(time.Now()); err != nil {
		t.Fatalf("after a refresh: %v", err)
	}
	if meta[images.DBYARA].Release != "20261115" {
		t.Fatalf("yara meta = %+v", meta[images.DBYARA])
	}
	if b, err := os.ReadFile(filepath.Join(dbDir, "osv", "osv-scanner", "npm", "all.zip")); err != nil || string(b) != "zip" {
		t.Fatalf("osv archive = %q, %v", b, err)
	}
	for _, c := range docker.calls {
		if slices.Contains(c, "none") {
			t.Fatalf("the refresh job ran without a network: %q", c)
		}
	}
	if len(docker.calls) != 2 {
		t.Fatalf("docker calls = %q, want trivy and freshclam", docker.calls)
	}
	rescans := 0
	for _, c := range f.scanner.Calls {
		if c.Stage == images.ScanStageSBOM {
			rescans++
		}
	}
	if rescans != 1 {
		t.Fatalf("re-scans after the refresh = %d", rescans)
	}
	// A second step within the day neither refreshes nor re-scans.
	m.Step(context.Background())
	if len(docker.calls) != 2 {
		t.Fatalf("refreshed again within the interval: %d calls", len(docker.calls))
	}
}

func TestYARARefreshKeepsLastBundleOnBadChecksum(t *testing.T) {
	z := yaraZip(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/release" {
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "x", "assets": []map[string]string{{
				"name": "yara-forge-rules-core.zip", "digest": "sha256:" + strings.Repeat("0", 64), "browser_download_url": srv.URL + "/core.zip",
			}}})
			return
		}
		_, _ = w.Write(z)
	}))
	defer srv.Close()
	dbDir := t.TempDir()
	rules := filepath.Join(dbDir, "yara", "rules.yar")
	_ = os.MkdirAll(filepath.Dir(rules), 0o700)
	_ = os.WriteFile(rules, []byte("last bundle"), 0o600)
	r := images.Refresher{DBDir: dbDir, Docker: (&fakeDocker{}).run, OSVBase: srv.URL, OSVEcosystems: []string{}, YARARelease: srv.URL + "/release"}
	if err := r.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "yara") {
		t.Fatalf("Refresh = %v, want the yara checksum failure", err)
	}
	if b, _ := os.ReadFile(rules); string(b) != "last bundle" {
		t.Fatalf("rules = %q, want the last bundle kept", b)
	}
}

func writeTarGz(w io.Writer, files map[string]string) error {
	zw := gzip.NewWriter(w)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			return err
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

func TestInstallDBFromLaptop(t *testing.T) {
	dbDir := t.TempDir()
	archive := filepath.Join(t.TempDir(), "clamav.tar.gz")
	var buf bytes.Buffer
	if err := writeTarGz(&buf, map[string]string{"main.cvd": "sigs"}); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(archive, buf.Bytes(), 0o600)
	sum := sha256.Sum256(buf.Bytes())
	now := time.Now()
	in := images.DBInstall{Name: images.DBClamAV, Archive: archive, SHA256: hex.EncodeToString(sum[:]), BuiltAt: now.Add(-time.Hour)}
	bad := in
	bad.SHA256 = strings.Repeat("0", 64)
	if err := images.InstallDB(dbDir, bad, now); err == nil {
		t.Fatal("a bundle with the wrong checksum installed")
	}
	old := in
	old.BuiltAt = now.Add(-8 * 24 * time.Hour)
	if err := images.InstallDB(dbDir, old, now); err == nil {
		t.Fatal("a bundle over 7 days old installed")
	}
	if err := images.InstallDB(dbDir, in, now); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dbDir, "clamav", "main.cvd")); err != nil || string(b) != "sigs" {
		t.Fatalf("installed = %q, %v", b, err)
	}
	meta, _ := images.ReadDBMeta(dbDir)
	if meta[images.DBClamAV].Source != images.DBSourceLaptop {
		t.Fatalf("meta = %+v", meta[images.DBClamAV])
	}
}
