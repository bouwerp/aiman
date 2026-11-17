package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness/images"
)

// dbPushDocker answers the scanner image build and, for a run, writes what
// trivy or freshclam would into the mounted database directory.
type dbPushDocker struct {
	mu       sync.Mutex
	built    bool
	failRun  string
	commands [][]string
}

var mountSource = regexp.MustCompile(`source=([^,]+),target=`)

func (d *dbPushDocker) run(_ context.Context, args []string) (string, int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.commands = append(d.commands, args)
	switch args[0] {
	case "image":
		if !d.built {
			return "no such image", 1, nil
		}
		return "sha256:scanner\n", 0, nil
	case "build":
		d.built = true
		return "", 0, nil
	case "run":
		joined := strings.Join(args, " ")
		if d.failRun != "" && strings.Contains(joined, d.failRun) {
			return "boom", 2, nil
		}
		m := mountSource.FindStringSubmatch(joined)
		if m == nil {
			return "no mount", 1, nil
		}
		files := map[string]string{"main.cvd": "cvd", "daily.cld": "cld"}
		if strings.Contains(joined, "--download-db-only") {
			files = map[string]string{"db/trivy.db": "bolt", "db/metadata.json": "{}"}
		}
		for name, body := range files {
			p := filepath.Join(m[1], name)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				return err.Error(), 1, nil
			}
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				return err.Error(), 1, nil
			}
		}
		return "", 0, nil
	}
	return "unexpected", 1, nil
}

// dbPushHost stands in for the remote: an upload keeps the bundle, and the
// db-install command runs images.InstallDB on it into a host database dir.
type dbPushHost struct {
	t        *testing.T
	dbDir    string
	uploads  map[string][]byte
	commands []string
	failCopy bool
}

var installRe = regexp.MustCompile(`db-install '?(\w+)'? "\$HOME"/'([^']+)' --sha256 ([0-9a-f]{64}) --built-at (\S+);`)

func (h *dbPushHost) ExecuteWithTimeout(_ context.Context, cmd string, _ time.Duration) (string, error) {
	h.commands = append(h.commands, cmd)
	m := installRe.FindStringSubmatch(cmd)
	if m == nil {
		return "", nil
	}
	built, err := time.Parse(time.RFC3339, m[4])
	if err != nil {
		return "", err
	}
	path := "$HOME/" + m[2]
	archive := filepath.Join(h.t.TempDir(), "bundle.tar.gz")
	if err := os.WriteFile(archive, h.uploads[path], 0o600); err != nil {
		return "", err
	}
	delete(h.uploads, path)
	return "", images.InstallDB(h.dbDir, images.DBInstall{Name: m[1], Archive: archive, SHA256: m[3], BuiltAt: built}, time.Now())
}

func (h *dbPushHost) UploadFile(_ context.Context, local, path string, _ time.Duration) error {
	if h.failCopy {
		return errors.New("connection reset")
	}
	b, err := os.ReadFile(local) //nolint:gosec // G304: the test's bundle
	if err != nil {
		return err
	}
	h.uploads[path] = b
	return nil
}

func dbPushServer(t *testing.T) *httptest.Server {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("packages/core/yara-rules-core.yar")
	_, _ = w.Write([]byte("rule x { condition: false }"))
	_ = zw.Close()
	z := buf.Bytes()
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
			_, _ = w.Write([]byte("zip " + r.URL.Path))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type dbPushFixture struct {
	docker *dbPushDocker
	host   *dbPushHost
	tmp    string
	out    *bytes.Buffer
	errOut *bytes.Buffer
	deps   dbPushDeps
}

func newDBPushFixture(t *testing.T) *dbPushFixture {
	t.Helper()
	srv := dbPushServer(t)
	f := &dbPushFixture{
		docker: &dbPushDocker{},
		host:   &dbPushHost{t: t, dbDir: t.TempDir(), uploads: map[string][]byte{}},
		tmp:    t.TempDir(),
		out:    &bytes.Buffer{},
		errOut: &bytes.Buffer{},
	}
	f.deps = dbPushDeps{
		remote: f.host, docker: f.docker.run,
		refresher: images.Refresher{
			HTTP: srv.Client(), OSVBase: srv.URL, OSVEcosystems: []string{"npm", "Go"},
			YARARelease: srv.URL + "/release",
		},
		tempParent: f.tmp,
		now:        func() time.Time { return time.Now().Add(-time.Minute) },
		out:        f.out, errOut: f.errOut,
	}
	return f
}

// assertLaptopClean fails when db-push left anything in its temp parent.
func (f *dbPushFixture) assertLaptopClean(t *testing.T) {
	t.Helper()
	left, err := os.ReadDir(f.tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("db-push left %d entries on the laptop: %v", len(left), left[0].Name())
	}
}

func TestDBPushInstallsEveryDatabaseOnTheHost(t *testing.T) {
	f := newDBPushFixture(t)
	if err := harnessDBPush(context.Background(), nil, f.deps); err != nil {
		t.Fatalf("db-push: %v\n%s", err, f.errOut)
	}
	f.assertLaptopClean(t)
	meta, err := images.ReadDBMeta(f.host.dbDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range dbPushAll {
		if meta[name].Source != images.DBSourceLaptop || meta[name].SHA256 == "" {
			t.Errorf("%s meta = %+v, want a laptop install with its sha256", name, meta[name])
		}
		if !strings.Contains(f.out.String(), "pushed "+name+": ") {
			t.Errorf("output does not report %s:\n%s", name, f.out)
		}
	}
	if err := meta.CheckFresh(time.Now()); err != nil {
		t.Errorf("host databases after the push: %v", err)
	}
	for _, p := range []string{
		"trivy/db/trivy.db", "trivy/db/metadata.json", "clamav/main.cvd", "clamav/daily.cld",
		"osv/osv-scanner/npm/all.zip", "osv/osv-scanner/Go/all.zip", "yara/rules.yar",
	} {
		if _, err := os.Stat(filepath.Join(f.host.dbDir, p)); err != nil {
			t.Errorf("host is missing %s: %v", p, err)
		}
	}
	if len(f.host.uploads) != 0 {
		t.Errorf("bundles left on the host: %v", f.host.uploads)
	}
	for _, c := range f.host.commands {
		if strings.Contains(c, "db-install") && !strings.Contains(c, "; rc=$?; rm -f ") {
			t.Errorf("db-install does not remove the remote bundle: %s", c)
		}
	}
	if !f.docker.built {
		t.Error("the scanner image was not built from the pinned Dockerfile")
	}
	for _, c := range f.docker.commands {
		if c[0] == "run" && !strings.Contains(strings.Join(c, " "), "TMPDIR=/scratch") {
			t.Errorf("refresh tool runs without its on-disk TMPDIR: %v", c)
		}
	}
}

func TestDBPushOnlyNamedSkipsScannerImage(t *testing.T) {
	f := newDBPushFixture(t)
	if err := harnessDBPush(context.Background(), []string{"yara", "osv"}, f.deps); err != nil {
		t.Fatalf("db-push: %v", err)
	}
	if len(f.docker.commands) != 0 {
		t.Errorf("osv and yara ran docker: %v", f.docker.commands)
	}
	if !strings.Contains(f.out.String(), "pushed osv") || !strings.Contains(f.out.String(), "pushed yara") {
		t.Errorf("output:\n%s", f.out)
	}
	if strings.Index(f.out.String(), "pushed osv") > strings.Index(f.out.String(), "pushed yara") {
		t.Errorf("pushes are not in the fixed order:\n%s", f.out)
	}
	f.assertLaptopClean(t)
}

func TestDBPushReportsAFailureAndPushesTheRest(t *testing.T) {
	f := newDBPushFixture(t)
	f.docker.failRun = "freshclam"
	err := harnessDBPush(context.Background(), []string{"trivy", "clamav"}, f.deps)
	if err == nil || !strings.Contains(err.Error(), "clamav") {
		t.Fatalf("err = %v, want the clamav failure", err)
	}
	if !strings.Contains(f.out.String(), "pushed trivy") || strings.Contains(f.out.String(), "pushed clamav") {
		t.Errorf("output:\n%s", f.out)
	}
	f.assertLaptopClean(t)
}

func TestDBPushFailedCopyCleansBothSides(t *testing.T) {
	f := newDBPushFixture(t)
	f.host.failCopy = true
	if err := harnessDBPush(context.Background(), []string{"yara"}, f.deps); err == nil {
		t.Fatal("a failed copy succeeded")
	}
	last := f.host.commands[len(f.host.commands)-1]
	if !strings.HasPrefix(last, "rm -f ") || !strings.Contains(last, ".part") {
		t.Errorf("no remote cleanup after a failed copy: %q", last)
	}
	f.assertLaptopClean(t)
}

func TestDBPushNames(t *testing.T) {
	got, err := dbPushNames([]string{"yara", "trivy", "yara"})
	if err != nil || strings.Join(got, ",") != "trivy,yara" {
		t.Errorf("names = %v, %v", got, err)
	}
	if _, err := dbPushNames([]string{"grype"}); err == nil {
		t.Error("an unknown database was accepted")
	}
}
