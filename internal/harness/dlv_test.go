package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeGo answers `go mod download -json` with sum and makes `go install`
// write body into GOBIN/dlv. It records each call's args and env.
type fakeGo struct {
	mu    sync.Mutex
	sum   string
	body  string
	calls [][]string
	envs  [][]string
}

func (f *fakeGo) run(_ context.Context, _ string, env []string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	f.envs = append(f.envs, env)
	switch {
	case len(args) >= 2 && args[0] == "mod" && args[1] == "download":
		return json.Marshal(map[string]string{"Path": DlvModule, "Version": args[len(args)-1], "Sum": f.sum})
	case len(args) >= 1 && args[0] == "install":
		bin := envValue(env, "GOBIN")
		if bin == "" {
			return nil, errors.New("no GOBIN")
		}
		if err := os.MkdirAll(bin, 0o700); err != nil {
			return nil, err
		}
		return nil, os.WriteFile(filepath.Join(bin, "dlv"), []byte(f.body), 0o700) //nolint:gosec // G306: a test binary
	}
	return nil, errors.New("unexpected go call " + strings.Join(args, " "))
}

func (f *fakeGo) installs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c[0] == "install" {
			n++
		}
	}
	return n
}

func envValue(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val
		}
	}
	return v
}

func TestDlvChecksum(t *testing.T) {
	pin := DlvPin{Version: "v1.27.2", ModuleSum: "h1:dhcjFjiVoQI53uwsG7EqYiMhdvFuywsBZx4KDOK+sYE="}

	t.Run("matching module sum builds once and caches", func(t *testing.T) {
		t.Setenv("GONOSUMDB", "*")
		t.Setenv("GOFLAGS", "-insecure")
		t.Setenv("GOPROXY", "http://evil.example")
		fg := &fakeGo{sum: pin.ModuleSum, body: "dlv-binary"}
		tools := &DlvTools{Dir: t.TempDir(), Pin: pin, Go: fg.run}
		path, err := tools.Ensure(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path) //nolint:gosec // G304: the test's own temp dir
		if err != nil || string(b) != "dlv-binary" {
			t.Fatalf("dlv at %s = %q, %v", path, b, err)
		}
		m, err := readDlvManifest(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		want := sha256.Sum256([]byte("dlv-binary"))
		if m.SHA256 != hex.EncodeToString(want[:]) || m.ModuleSum != pin.ModuleSum || m.Version != pin.Version {
			t.Fatalf("manifest = %+v", m)
		}
		env := fg.envs[0]
		for k, v := range map[string]string{
			"GOPROXY": "https://proxy.golang.org", "GOSUMDB": "sum.golang.org", "GONOSUMDB": "",
			"GOFLAGS": "-modcacherw", "CGO_ENABLED": "0", "GOWORK": "off", "GOINSECURE": "", "GOPRIVATE": "",
		} {
			if got := envValue(env, k); got != v {
				t.Errorf("env %s = %q, want %q", k, got, v)
			}
		}
		if !slices.Contains(fg.calls[1], "-trimpath") || fg.calls[1][len(fg.calls[1])-1] != DlvPackage+"@"+pin.Version {
			t.Fatalf("install args = %v", fg.calls[1])
		}
		if _, err := tools.Ensure(context.Background()); err != nil || fg.installs() != 1 {
			t.Fatalf("second Ensure: err %v, installs %d; want the cache", err, fg.installs())
		}
	})

	t.Run("module sum mismatch installs nothing", func(t *testing.T) {
		fg := &fakeGo{sum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", body: "evil"}
		dir := t.TempDir()
		tools := &DlvTools{Dir: dir, Pin: pin, Go: fg.run}
		_, err := tools.Ensure(context.Background())
		if !errors.Is(err, ErrDlvChecksum) {
			t.Fatalf("err = %v, want ErrDlvChecksum", err)
		}
		if fg.installs() != 0 {
			t.Fatal("go install ran after a checksum mismatch")
		}
		if _, err := os.Stat(filepath.Join(tools.binDir(), "dlv")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("dlv exists after a mismatch: %v", err)
		}
		left, _ := os.ReadDir(dir)
		for _, e := range left {
			if strings.HasPrefix(e.Name(), ".build-") {
				t.Fatalf("build directory %s left behind", e.Name())
			}
		}
	})

	t.Run("tampered cache is rebuilt", func(t *testing.T) {
		fg := &fakeGo{sum: pin.ModuleSum, body: "dlv-binary"}
		tools := &DlvTools{Dir: t.TempDir(), Pin: pin, Go: fg.run}
		path, err := tools.Ensure(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("swapped"), 0o700); err != nil { //nolint:gosec // G306: a test binary
			t.Fatal(err)
		}
		if _, err := tools.Ensure(context.Background()); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(path) //nolint:gosec // G304: the test's own temp dir
		if string(b) != "dlv-binary" || fg.installs() != 2 {
			t.Fatalf("after tamper: %q, installs %d", b, fg.installs())
		}
	})

	t.Run("cache built for another pin is rebuilt", func(t *testing.T) {
		fg := &fakeGo{sum: pin.ModuleSum, body: "dlv-binary"}
		dir := t.TempDir()
		tools := &DlvTools{Dir: dir, Pin: pin, Go: fg.run}
		path, err := tools.Ensure(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		m, _ := readDlvManifest(filepath.Dir(path))
		m.ModuleSum = "h1:other"
		if err := writeDlvManifest(filepath.Dir(path), m); err != nil {
			t.Fatal(err)
		}
		if _, err := tools.Ensure(context.Background()); err != nil || fg.installs() != 2 {
			t.Fatalf("err %v, installs %d", err, fg.installs())
		}
	})
}

func TestDlvPinFromConfig(t *testing.T) {
	if p, err := NewDlvPin("", ""); err != nil || p != DefaultDlv {
		t.Fatalf("empty = %+v, %v", p, err)
	}
	if p, err := NewDlvPin(DefaultDlv.Version, ""); err != nil || p != DefaultDlv {
		t.Fatalf("default version = %+v, %v", p, err)
	}
	if _, err := NewDlvPin("v1.26.0", ""); err == nil {
		t.Fatal("another version without a sum was accepted")
	}
	if _, err := NewDlvPin("latest", "h1:dhcjFjiVoQI53uwsG7EqYiMhdvFuywsBZx4KDOK+sYE="); err == nil {
		t.Fatal("a non-semver version was accepted")
	}
	if _, err := NewDlvPin("v1.26.0", "sha256:abc"); err == nil {
		t.Fatal("a sum that is not h1 was accepted")
	}
	if p, err := NewDlvPin("v1.26.0", "h1:dhcjFjiVoQI53uwsG7EqYiMhdvFuywsBZx4KDOK+sYE="); err != nil || p.Version != "v1.26.0" {
		t.Fatalf("pinned other = %+v, %v", p, err)
	}
}
