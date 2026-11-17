package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// exampleFile is the harness file from the design, without the phase 5 stub.
const exampleFile = `version: 1
app:
  build: go build -o /out/app ./cmd/app
  run: /out/app serve --addr :8080
  ports: [{name: web, container: 8080}]
  limits: {memory: 2g, cpus: 2, pids: 256}
  debug: {lang: go}
services:
  db:
    image: postgres:16
    env: {POSTGRES_PASSWORD: test, PGPORT: 5432}
    ready: pg_isready -h db -U postgres
    database: {kind: postgres, name: app}
    root: true
    caps: [CHOWN, SETUID, SETGID, DAC_OVERRIDE, FOWNER]
    limits: {memory: 1g}
scenarios:
  - id: refund-happy-path
    title: Refund a paid order
    services: [db]
    setup: [{run: scripts/harness/migrate.sh}]
    fixtures: [{sql: testdata/harness/refund.sql, into: db}]
    steps:
      - do: "Open {{web}}/orders/42 and press Refund."
        expect: "The order shows Refunded. The balance is 0."
    checks:
      - {name: refund row, run: "scripts/harness/check-refund.sh 42"}
    stubs:
      - {host: api.stripe.com, path: /v1/refunds, status: 200, body_file: testdata/harness/stripe-refund.json}
`

func codeOf(t *testing.T, err error) *Error {
	t.Helper()
	var he *Error
	if !errors.As(err, &he) {
		t.Fatalf("error %v (%T) is not a *harness.Error", err, err)
	}
	return he
}

func wantInvalid(t *testing.T, err error, fragment string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s containing %q, got nil", CodeInvalid, fragment)
	}
	he := codeOf(t, err)
	if he.Code != CodeInvalid {
		t.Fatalf("code = %s, want %s (%v)", he.Code, CodeInvalid, err)
	}
	for _, e := range he.Errors {
		if strings.Contains(e, fragment) {
			return
		}
	}
	t.Fatalf("no error contains %q: %q", fragment, he.Errors)
}

func TestParseExample(t *testing.T) {
	f, err := Parse([]byte(exampleFile))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Version != 1 || f.App.Run != "/out/app serve --addr :8080" {
		t.Fatalf("app = %+v", f.App)
	}
	if len(f.App.Ports) != 1 || f.App.Ports[0] != (Port{Name: "web", Container: 8080}) {
		t.Fatalf("ports = %+v", f.App.Ports)
	}
	if f.App.Debug == nil || f.App.Debug.Lang != "go" {
		t.Fatalf("debug = %+v", f.App.Debug)
	}
	db, ok := f.Services["db"]
	if !ok {
		t.Fatal("service db missing")
	}
	if db.Image != "postgres:16" || !db.Root || len(db.Caps) != 5 {
		t.Fatalf("db = %+v", db)
	}
	if db.Env["PGPORT"] != "5432" {
		t.Fatalf("numeric env value = %q, want 5432", db.Env["PGPORT"])
	}
	if db.Database == nil || db.Database.Kind != "postgres" || db.Database.Name != "app" {
		t.Fatalf("database = %+v", db.Database)
	}
	sc, ok := f.Scenario("refund-happy-path")
	if !ok {
		t.Fatal("scenario missing")
	}
	if sc.Title != "Refund a paid order" || len(sc.Steps) != 1 || len(sc.Checks) != 1 || len(sc.Stubs) != 1 {
		t.Fatalf("scenario = %+v", sc)
	}
	if sc.Fixtures[0] != (Fixture{SQL: "testdata/harness/refund.sql", Into: "db"}) {
		t.Fatalf("fixture = %+v", sc.Fixtures[0])
	}
	if sc.Stubs[0].BodyFile != "testdata/harness/stripe-refund.json" {
		t.Fatalf("stub = %+v", sc.Stubs[0])
	}
	if _, ok := f.Scenario("nope"); ok {
		t.Fatal("unknown scenario found")
	}
}

func TestParseRejectsUnknownKey(t *testing.T) {
	_, err := Parse([]byte(strings.Replace(exampleFile, "  build:", "  bulid: x\n  build:", 1)))
	wantInvalid(t, err, "bulid")
}

func TestParseRejectsEmptyAndMultiDoc(t *testing.T) {
	_, err := Parse(nil)
	wantInvalid(t, err, "empty")
	_, err = Parse([]byte(exampleFile + "---\nversion: 1\n"))
	wantInvalid(t, err, "one YAML document")
}

func TestParseRejectsTooBig(t *testing.T) {
	_, err := Parse([]byte(exampleFile + "#" + strings.Repeat("x", MaxFileBytes)))
	wantInvalid(t, err, "larger than")
}

func TestParseValidation(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		{"version", "version: 1", "version: 2", "version"},
		{"no run", "  run: /out/app serve --addr :8080\n", "", "app.run"},
		{"bad port", "container: 8080", "container: 70000", "container"},
		{"dup port", "ports: [{name: web, container: 8080}]", "ports: [{name: web, container: 8080}, {name: web, container: 81}]", "duplicate"},
		{"reserved service", "  db:\n", "  edge:\n", "reserved"},
		{"bad service name", "  db:\n", "  Db_1:\n", "service name"},
		{"no image", "    image: postgres:16\n", "", "image"},
		{"image whitespace", "image: postgres:16", "image: \"postgres:16 --privileged\"", "image"},
		{"bad cap", "FOWNER]", "FOWNER, NET_ADMIN]", "NET_ADMIN"},
		{"bad db kind", "kind: postgres", "kind: mysql", "database.kind"},
		{"bad memory", "{memory: 1g}", "{memory: lots}", "memory"},
		{"infinite memory", "{memory: 1g}", "{memory: infinity}", "memory"},
		{"negative cpus", "{memory: 1g}", "{cpus: -1}", "cpus"},
		{"negative pids", "{memory: 1g}", "{pids: -1}", "pids"},
		{"bad debug", "lang: go", "lang: rust", "debug.lang"},
		{"no scenario id", "  - id: refund-happy-path\n    title: Refund a paid order\n", "  - title: x\n", "id"},
		{"unknown service", "services: [db]", "services: [db, cache]", "cache"},
		{"fixture into", "into: db", "into: other", "into"},
		{"fixture two kinds", "{sql: testdata/harness/refund.sql, into: db}", "{sql: a.sql, script: b.sh, into: db}", "exactly one"},
		{"fixture escapes", "sql: testdata/harness/refund.sql", "sql: ../outside.sql", "escapes"},
		{"fixture absolute", "sql: testdata/harness/refund.sql", "sql: /etc/passwd", "relative"},
		{"empty setup", "setup: [{run: scripts/harness/migrate.sh}]", "setup: [{run: \"\"}]", "setup"},
		{"step without do", "      - do: \"Open {{web}}/orders/42 and press Refund.\"\n", "      - do: \"\"\n", "do"},
		{"unknown placeholder", "{{web}}", "{{api}}", "{{api}}"},
		{"check without run", "run: \"scripts/harness/check-refund.sh 42\"", "run: \"\"", "run"},
		{"stub path", "path: /v1/refunds", "path: v1/refunds", "path"},
		{"stub status", "status: 200", "status: 999", "status"},
		{"stub body escapes", "body_file: testdata/harness/stripe-refund.json", "body_file: ../x.json", "escapes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.Replace(exampleFile, tc.from, tc.to, 1)
			if src == exampleFile {
				t.Fatalf("replacement %q not found", tc.from)
			}
			_, err := Parse([]byte(src))
			wantInvalid(t, err, tc.want)
		})
	}
}

func TestParseNoScenarios(t *testing.T) {
	_, err := Parse([]byte("version: 1\napp: {run: /bin/true}\n"))
	wantInvalid(t, err, "scenario")
}

func TestParseDuplicateScenario(t *testing.T) {
	src := "version: 1\napp: {run: x}\nscenarios:\n  - {id: a, steps: [{do: x}]}\n  - {id: a, steps: [{do: y}]}\n"
	_, err := Parse([]byte(src))
	wantInvalid(t, err, "duplicate")
}

func TestParseCollectsEveryError(t *testing.T) {
	src := strings.Replace(exampleFile, "version: 1", "version: 3", 1)
	src = strings.Replace(src, "kind: postgres", "kind: mysql", 1)
	_, err := Parse([]byte(src))
	he := codeOf(t, err)
	if len(he.Errors) < 2 {
		t.Fatalf("errors = %q, want both", he.Errors)
	}
	if he.Data()["errors"] == nil {
		t.Fatalf("data = %v, want errors", he.Data())
	}
}

func TestParseNormalisesCaps(t *testing.T) {
	src := strings.Replace(exampleFile, "[CHOWN, SETUID", "[cap_chown, setuid", 1)
	f, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Services["db"].Caps[:2]; got[0] != "CHOWN" || got[1] != "SETUID" {
		t.Fatalf("caps = %q", got)
	}
}

func TestRejectsUnsafeService(t *testing.T) {
	cases := []struct {
		name, insert, want string
	}{
		{"privileged", "    privileged: true\n", "privileged"},
		{"network_mode", "    network_mode: host\n", "network_mode"},
		{"docker socket", "    volumes: [/var/run/docker.sock:/var/run/docker.sock]\n", "volumes"},
		{"cap_add", "    cap_add: [SYS_ADMIN]\n", "cap_add"},
		{"pid", "    pid: host\n", "pid"},
		{"devices", "    devices: [/dev/kvm]\n", "devices"},
		{"user", "    user: \"0:0\"\n", "user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.Replace(exampleFile, "    root: true\n", "    root: true\n"+tc.insert, 1)
			_, err := Parse([]byte(src))
			wantInvalid(t, err, tc.want+" is not allowed")
		})
	}
	t.Run("cap outside the five", func(t *testing.T) {
		for _, c := range []string{"SYS_ADMIN", "NET_RAW", "ALL", "SYS_PTRACE"} {
			src := strings.Replace(exampleFile, "FOWNER]", "FOWNER, "+c+"]", 1)
			_, err := Parse([]byte(src))
			wantInvalid(t, err, c)
		}
	})
	t.Run("app privileged", func(t *testing.T) {
		src := strings.Replace(exampleFile, "  build:", "  privileged: true\n  build:", 1)
		_, err := Parse([]byte(src))
		wantInvalid(t, err, "privileged is not allowed")
	})
}

func TestImageAllowed(t *testing.T) {
	cases := []struct {
		ref  string
		want bool
	}{
		{"postgres:16", true},
		{"docker.io/library/postgres:16", true},
		{"library/postgres:16", true},
		{"postgres:16.1", false},
		{"postgres", false},
		{"postgres:15", false},
		{"golang:1.24", true},
		{"golang:1.24-alpine", true},
		{"golang", true},
		{"golang@sha256:" + strings.Repeat("a", 64), true},
		{"docker.io/golang:1.24", true},
		{"golangx:1", false},
		{"evil/golang:1.24", false},
		{"ghcr.io/golang:1.24", false},
		{"node:lts", true},
		{"node:22@sha256:" + strings.Repeat("b", 64), true},
		{"redis:7", false},
		{"", false},
		{"NODE:22", false},
	}
	for _, tc := range cases {
		if got := ImageAllowed(tc.ref, DefaultAllowedImages); got != tc.want {
			t.Errorf("ImageAllowed(%q) = %v, want %v", tc.ref, got, tc.want)
		}
	}
	if !ImageAllowed("ghcr.io/acme/tools:1", []string{"ghcr.io/acme/*:*"}) {
		t.Error("ghcr.io/acme/*:* should allow ghcr.io/acme/tools:1")
	}
	if ImageAllowed("ghcr.io/acme/x/y:1", []string{"ghcr.io/acme/*:*"}) {
		t.Error("* must not cross a path separator")
	}
	if !ImageAllowed("localhost:5000/app:dev", []string{"localhost:5000/app:*"}) {
		t.Error("registry port must not be read as a tag")
	}
	if ImageAllowed("postgres:16", nil) {
		t.Error("an empty list allows nothing")
	}
}

func TestLimitsSum(t *testing.T) {
	f, err := Parse([]byte(exampleFile))
	if err != nil {
		t.Fatal(err)
	}
	// app 2g + db 1g + edge 256m.
	want := int64(2<<30 + 1<<30 + 256<<20)
	sum, err := f.MemorySum("refund-happy-path", 0)
	if err != nil {
		t.Fatal(err)
	}
	if sum != want {
		t.Fatalf("sum = %d, want %d", sum, want)
	}
	if err := f.CheckLimits("refund-happy-path", LimitOptions{}); err != nil {
		t.Fatalf("under the default 6G: %v", err)
	}
	err = f.CheckLimits("refund-happy-path", LimitOptions{RunMemory: 3 << 30})
	he := codeOf(t, err)
	if he.Code != CodeTooLarge || he.Sum != want || he.Limit != 3<<30 {
		t.Fatalf("err = %+v", he)
	}
	if d := he.Data(); d["sum"] != want || d["limit"] != int64(3<<30) {
		t.Fatalf("data = %v", d)
	}

	t.Run("service default from service memory", func(t *testing.T) {
		src := strings.Replace(exampleFile, "    limits: {memory: 1g}\n", "", 1)
		f, err := Parse([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		sum, _ := f.MemorySum("refund-happy-path", 0)
		if sum != 2<<30+DefaultServiceMemory+256<<20 {
			t.Fatalf("sum with default service memory = %d", sum)
		}
		sum, _ = f.MemorySum("refund-happy-path", 512<<20)
		if sum != 2<<30+512<<20+256<<20 {
			t.Fatalf("sum with service memory 512M = %d", sum)
		}
	})
	t.Run("only the scenario's services count", func(t *testing.T) {
		src := strings.Replace(exampleFile, "services: [db]", "services: []", 1)
		src = strings.Replace(src, "    fixtures: [{sql: testdata/harness/refund.sql, into: db}]\n", "", 1)
		f, err := Parse([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		sum, _ := f.MemorySum("refund-happy-path", 0)
		if sum != 2<<30+256<<20 {
			t.Fatalf("sum = %d", sum)
		}
	})
	t.Run("unknown scenario", func(t *testing.T) {
		_, err := f.MemorySum("nope", 0)
		if codeOf(t, err).Code != CodeUnknownScenario {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("resolved limits", func(t *testing.T) {
		app := f.AppLimits()
		if app != (ResolvedLimits{Memory: 2 << 30, CPUs: 2, PIDs: 256}) {
			t.Fatalf("app = %+v", app)
		}
		db := f.ServiceLimits("db", 0)
		if db != (ResolvedLimits{Memory: 1 << 30, CPUs: 2, PIDs: 256}) {
			t.Fatalf("db = %+v", db)
		}
		if EdgeLimits != (ResolvedLimits{Memory: 256 << 20, CPUs: 0.5, PIDs: 64}) {
			t.Fatalf("edge = %+v", EdgeLimits)
		}
	})
}

// --- git-backed loading ---

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "README.md", "hi\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func commitAll(t *testing.T, dir, msg string) string {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", msg)
	return git(t, dir, "rev-parse", "HEAD")
}

func TestLoadAtSHA(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	noFile := git(t, dir, "rev-parse", "HEAD")

	_, err := LoadAtSHA(ctx, dir, noFile, nil)
	if codeOf(t, err).Code != CodeNoFile {
		t.Fatalf("err = %v, want %s", err, CodeNoFile)
	}

	writeFile(t, dir, FilePath, exampleFile)
	sha := commitAll(t, dir, "harness")

	// The working tree copy is never read for a load at a sha.
	writeFile(t, dir, FilePath, "version: 9\n")

	got, err := LoadAtSHA(ctx, dir, sha[:8], nil)
	if err != nil {
		t.Fatalf("LoadAtSHA: %v", err)
	}
	if got.SHA != sha || got.Source != SourceFile || got.Dirty || got.DiffSHA256 != "" {
		t.Fatalf("loaded = %+v", got)
	}
	if _, ok := got.File.Scenario("refund-happy-path"); !ok {
		t.Fatal("scenario missing")
	}

	// An invalid committed file is harness_invalid.
	writeFile(t, dir, FilePath, "version: 1\napp: {run: x, privileged: true}\n")
	bad := commitAll(t, dir, "bad")
	_, err = LoadAtSHA(ctx, dir, bad, nil)
	wantInvalid(t, err, "privileged is not allowed")

	// The old commit still loads.
	if _, err := LoadAtSHA(ctx, dir, sha, nil); err != nil {
		t.Fatalf("old sha: %v", err)
	}

	_, err = LoadAtSHA(ctx, dir, "deadbeefdeadbeef", nil)
	if err == nil {
		t.Fatal("unknown sha loaded")
	}
}

func TestLoadAtSHARefusesSymlink(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	writeFile(t, dir, "other.yaml", exampleFile)
	if err := os.MkdirAll(filepath.Join(dir, ".aiman"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../other.yaml", filepath.Join(dir, FilePath)); err != nil {
		t.Fatal(err)
	}
	sha := commitAll(t, dir, "link")
	_, err := LoadAtSHA(ctx, dir, sha, nil)
	wantInvalid(t, err, "regular file")
	_, err = LoadDirty(ctx, dir, nil)
	wantInvalid(t, err, "regular file")
}

func TestLoadDirty(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	writeFile(t, dir, FilePath, exampleFile)
	head := commitAll(t, dir, "harness")

	clean, err := LoadDirty(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if clean.SHA != head || !clean.Dirty || clean.Source != SourceFile {
		t.Fatalf("clean = %+v", clean)
	}
	empty := sha256.Sum256(nil)
	if clean.DiffSHA256 != hex.EncodeToString(empty[:]) {
		t.Fatalf("clean diff hash = %s, want the hash of nothing", clean.DiffSHA256)
	}

	// The working tree copy is what a dirty load reads.
	edited := strings.Replace(exampleFile, "Refund a paid order", "Refund an order", 1)
	writeFile(t, dir, FilePath, edited)
	d1, err := LoadDirty(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	sc, _ := d1.File.Scenario("refund-happy-path")
	if sc.Title != "Refund an order" {
		t.Fatalf("title = %q, want the working tree's", sc.Title)
	}
	if d1.SHA != head || d1.DiffSHA256 == clean.DiffSHA256 || len(d1.DiffSHA256) != 64 {
		t.Fatalf("d1 = %+v", d1)
	}

	// Same changes give the same hash.
	d1b, _ := LoadDirty(ctx, dir, nil)
	if d1b.DiffSHA256 != d1.DiffSHA256 {
		t.Fatal("hash is not stable")
	}

	// An untracked file changes the hash, and so does its content.
	writeFile(t, dir, "notes/new.txt", "one\n")
	d2, _ := LoadDirty(ctx, dir, nil)
	if d2.DiffSHA256 == d1.DiffSHA256 {
		t.Fatal("untracked file did not change the hash")
	}
	writeFile(t, dir, "notes/new.txt", "two\n")
	d3, _ := LoadDirty(ctx, dir, nil)
	if d3.DiffSHA256 == d2.DiffSHA256 {
		t.Fatal("untracked content did not change the hash")
	}

	// An ignored file does not count.
	writeFile(t, dir, ".git/info/exclude", "*.log\n")
	writeFile(t, dir, "debug.log", "noise\n")
	d4, _ := LoadDirty(ctx, dir, nil)
	if d4.DiffSHA256 != d3.DiffSHA256 {
		t.Fatal("ignored file changed the hash")
	}

	// No file in the working tree is harness_no_file.
	if err := os.Remove(filepath.Join(dir, FilePath)); err != nil {
		t.Fatal(err)
	}
	_, err = LoadDirty(ctx, dir, nil)
	if codeOf(t, err).Code != CodeNoFile {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigOverride(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	writeFile(t, dir, FilePath, exampleFile)
	sha := commitAll(t, dir, "harness")

	override := []byte(strings.Replace(exampleFile, "Refund a paid order", "From config", 1))
	got, err := LoadAtSHA(ctx, dir, sha, override)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceConfig || got.SHA != sha {
		t.Fatalf("loaded = %+v", got)
	}
	if sc, _ := got.File.Scenario("refund-happy-path"); sc.Title != "From config" {
		t.Fatalf("title = %q, want the override's", sc.Title)
	}

	// The override wins even when the commit has no file.
	bare := newRepo(t)
	bareSHA := git(t, bare, "rev-parse", "HEAD")
	got, err = LoadAtSHA(ctx, bare, bareSHA, override)
	if err != nil || got.Source != SourceConfig {
		t.Fatalf("override without a file: %+v, %v", got, err)
	}

	// A dirty load takes the override too and keeps the diff hash.
	writeFile(t, dir, "x.txt", "x")
	got, err = LoadDirty(ctx, dir, override)
	if err != nil || got.Source != SourceConfig || !got.Dirty || got.DiffSHA256 == "" {
		t.Fatalf("dirty override: %+v, %v", got, err)
	}

	// An invalid override is harness_invalid, never a fallback to the file.
	_, err = LoadAtSHA(ctx, dir, sha, []byte("version: 1\nbogus: true\n"))
	wantInvalid(t, err, "bogus")

	// ParseOverride accepts an already decoded value.
	var v any
	if err := yaml.Unmarshal(override, &v); err != nil {
		t.Fatal(err)
	}
	b, err := OverrideBytes(v)
	if err != nil {
		t.Fatal(err)
	}
	got, err = LoadAtSHA(ctx, dir, sha, b)
	if err != nil || got.Source != SourceConfig {
		t.Fatalf("override from a decoded value: %+v, %v", got, err)
	}
	if b, err := OverrideBytes(nil); err != nil || b != nil {
		t.Fatalf("nil override = %q, %v", b, err)
	}
}

func TestSubmodulePath(t *testing.T) {
	t.Run("pure", func(t *testing.T) {
		subs := []string{"vendor/lib", "third_party/x"}
		cases := []struct {
			path string
			want bool
		}{
			{"vendor/lib/data.sql", true},
			{"vendor/lib", true},
			{"vendor/library/x.sql", false},
			{"testdata/a.sql", false},
			{"./vendor/lib/a.sql", true},
		}
		for _, tc := range cases {
			got, in := InSubmodule(tc.path, subs)
			if in != tc.want {
				t.Errorf("InSubmodule(%q) = %v, want %v", tc.path, in, tc.want)
			}
			if in && got == "" {
				t.Errorf("InSubmodule(%q) named no submodule", tc.path)
			}
		}
	})

	t.Run("paths of a file", func(t *testing.T) {
		f, err := Parse([]byte(exampleFile))
		if err != nil {
			t.Fatal(err)
		}
		paths := f.Paths()
		for _, want := range []string{"testdata/harness/refund.sql", "testdata/harness/stripe-refund.json", "scripts/harness/migrate.sh", "scripts/harness/check-refund.sh"} {
			found := false
			for _, p := range paths {
				if p == want {
					found = true
				}
			}
			if !found {
				t.Errorf("Paths() = %q, missing %q", paths, want)
			}
		}
	})

	ctx := context.Background()
	dir := newRepo(t)
	writeFile(t, dir, ".gitmodules", "[submodule \"lib\"]\n\tpath = vendor/lib\n\turl = https://example.com/lib.git\n")
	src := strings.Replace(exampleFile, "sql: testdata/harness/refund.sql", "sql: vendor/lib/refund.sql", 1)
	writeFile(t, dir, FilePath, src)
	sha := commitAll(t, dir, "submodule path")

	_, err := LoadAtSHA(ctx, dir, sha, nil)
	he := codeOf(t, err)
	if he.Code != CodeSubmodule || he.Path != "vendor/lib/refund.sql" {
		t.Fatalf("err = %+v", he)
	}
	if he.Data()["path"] != "vendor/lib/refund.sql" {
		t.Fatalf("data = %v", he.Data())
	}

	_, err = LoadDirty(ctx, dir, nil)
	if codeOf(t, err).Code != CodeSubmodule {
		t.Fatalf("dirty err = %v", err)
	}

	// A run command inside a submodule is refused too.
	src = strings.Replace(exampleFile, "run: scripts/harness/migrate.sh", "run: vendor/lib/migrate.sh --all", 1)
	writeFile(t, dir, FilePath, src)
	sha = commitAll(t, dir, "submodule script")
	_, err = LoadAtSHA(ctx, dir, sha, nil)
	if he := codeOf(t, err); he.Code != CodeSubmodule || he.Path != "vendor/lib/migrate.sh" {
		t.Fatalf("err = %+v", he)
	}

	// No path in a submodule loads.
	writeFile(t, dir, FilePath, exampleFile)
	sha = commitAll(t, dir, "fine")
	if _, err := LoadAtSHA(ctx, dir, sha, nil); err != nil {
		t.Fatalf("clean paths: %v", err)
	}
}

func TestErrorIs(t *testing.T) {
	err := &Error{Code: CodeNoFile, Msg: "x"}
	if !errors.Is(err, ErrNoFile) {
		t.Fatal("errors.Is by code failed")
	}
	if errors.Is(err, ErrInvalid) {
		t.Fatal("different codes matched")
	}
}
