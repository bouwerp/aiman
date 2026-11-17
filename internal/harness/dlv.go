package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Delve has no release binaries for Linux, so serve builds it on the host
// with `go install` from one pinned module version. The checksum story:
//
//  1. `go mod download -json` fetches the module zip from proxy.golang.org
//     into a private module cache, and the go command checks it against
//     sum.golang.org. Serve then compares the zip's h1 hash with the pin in
//     code (DlvPin.ModuleSum) and stops on any difference.
//  2. `go install <pkg>@<version>` builds with that module's own go.sum, so
//     every dependency is held to the hashes the pinned zip carries (and to
//     the checksum database). GOPROXY, GOSUMDB, GONOSUMDB, GOPRIVATE,
//     GOINSECURE and GOFLAGS are reset, so a host setting cannot skip either
//     check.
//  3. The binary's sha256 goes into dlv.json next to it. Each Ensure hashes
//     the cached binary again before a run mounts it, and rebuilds on a
//     mismatch. The binary hash itself cannot be pinned in code: it depends
//     on the host's Go toolchain.
//
// The binary is static (CGO_ENABLED=0) and is mounted read-only into the app
// container; nothing inside a container fetches it.

// DlvModule and DlvPackage name Delve's module and its command.
const (
	DlvModule  = "github.com/go-delve/delve"
	DlvPackage = DlvModule + "/cmd/dlv"
	// dlvBuildTimeout bounds the download and the build.
	dlvBuildTimeout = 10 * time.Minute
)

// DlvPin is one Delve release: its module version and the go.sum h1 hash of
// its module zip.
type DlvPin struct {
	Version   string
	ModuleSum string
}

// DefaultDlv is the Delve a debug run uses unless harness.dlv_version names
// another (with harness.dlv_module_sum).
var DefaultDlv = DlvPin{Version: "v1.27.2", ModuleSum: "h1:dhcjFjiVoQI53uwsG7EqYiMhdvFuywsBZx4KDOK+sYE="}

// ErrDlvChecksum is a downloaded Delve module whose hash is not the pin.
var ErrDlvChecksum = errors.New("delve module checksum mismatch")

var (
	semverRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	h1Re     = regexp.MustCompile(`^h1:[A-Za-z0-9+/]{43}=$`)
)

// NewDlvPin reads harness.dlv_version and harness.dlv_module_sum. Both empty,
// or the default version alone, is DefaultDlv; another version needs its sum.
func NewDlvPin(version, sum string) (DlvPin, error) {
	version, sum = strings.TrimSpace(version), strings.TrimSpace(sum)
	if version == "" && sum == "" {
		return DefaultDlv, nil
	}
	if version == DefaultDlv.Version && (sum == "" || sum == DefaultDlv.ModuleSum) {
		return DefaultDlv, nil
	}
	if !semverRe.MatchString(version) {
		return DlvPin{}, fmt.Errorf("harness.dlv_version %q must be vX.Y.Z", version)
	}
	if !h1Re.MatchString(sum) {
		return DlvPin{}, fmt.Errorf("harness.dlv_version %s needs harness.dlv_module_sum, the h1: hash go.sum lists for %s %s", version, DlvModule, version)
	}
	return DlvPin{Version: version, ModuleSum: sum}, nil
}

// GoRunner runs the go command in dir with exactly env and returns stdout.
type GoRunner func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error)

// DlvTools builds and caches the pinned Delve under Dir
// (~/.aiman/harness/tools).
type DlvTools struct {
	Dir string
	Pin DlvPin
	// Go runs the go command; nil is ExecGo.
	Go GoRunner

	mu sync.Mutex
}

// dlvManifest is dlv.json beside the cached binary.
type dlvManifest struct {
	Version   string    `json:"version"`
	ModuleSum string    `json:"module_sum"`
	SHA256    string    `json:"sha256"`
	Built     time.Time `json:"built"`
}

const dlvManifestName = "dlv.json"

func (t *DlvTools) binDir() string {
	return filepath.Join(t.Dir, fmt.Sprintf("dlv-%s-linux-%s", t.Pin.Version, runtime.GOARCH))
}

// Ensure returns the path of a verified dlv binary, building it first when
// the cache is missing, was built for another pin, or no longer matches its
// recorded sha256.
func (t *DlvTools) Ensure(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Pin.Version == "" {
		t.Pin = DefaultDlv
	}
	dir := t.binDir()
	path := filepath.Join(dir, "dlv")
	if t.cached(dir, path) {
		return path, nil
	}
	ctx, cancel := context.WithTimeout(ctx, dlvBuildTimeout)
	defer cancel()
	if err := t.build(ctx, dir); err != nil {
		return "", err
	}
	if !t.cached(dir, path) {
		return "", errors.New("delve: the built binary does not match its manifest")
	}
	return path, nil
}

func (t *DlvTools) cached(dir, path string) bool {
	m, err := readDlvManifest(dir)
	if err != nil || m.Version != t.Pin.Version || m.ModuleSum != t.Pin.ModuleSum {
		return false
	}
	sum, err := fileSHA256(path)
	return err == nil && sum == m.SHA256
}

// build downloads and checks the module, builds dlv in a scratch directory
// under Dir and moves it into dir with its manifest.
func (t *DlvTools) build(ctx context.Context, dir string) error {
	if err := os.MkdirAll(t.Dir, 0o700); err != nil {
		return err
	}
	scratch, err := os.MkdirTemp(t.Dir, ".build-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	run := t.Go
	if run == nil {
		run = ExecGo
	}
	env := dlvBuildEnv(os.Environ(), scratch)
	out, err := run(ctx, scratch, env, "mod", "download", "-json", DlvModule+"@"+t.Pin.Version)
	if err != nil {
		return fmt.Errorf("delve: download %s %s: %w", DlvModule, t.Pin.Version, err)
	}
	var dl struct {
		Sum   string
		Error string
	}
	if err := json.Unmarshal(out, &dl); err != nil {
		return fmt.Errorf("delve: read go mod download: %w", err)
	}
	if dl.Error != "" {
		return fmt.Errorf("delve: download %s %s: %s", DlvModule, t.Pin.Version, dl.Error)
	}
	if dl.Sum != t.Pin.ModuleSum {
		return fmt.Errorf("%w: %s %s is %s, the pin is %s", ErrDlvChecksum, DlvModule, t.Pin.Version, dl.Sum, t.Pin.ModuleSum)
	}
	if _, err := run(ctx, scratch, env, "install", "-trimpath", DlvPackage+"@"+t.Pin.Version); err != nil {
		return fmt.Errorf("delve: build %s %s: %w", DlvPackage, t.Pin.Version, err)
	}
	return t.place(filepath.Join(scratch, "bin", "dlv"), dir)
}

func (t *DlvTools) place(built, dir string) error {
	sum, err := fileSHA256(built)
	if err != nil {
		return fmt.Errorf("delve: hash the build: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// The manifest goes first, so a crash between the two leaves a mismatch
	// that the next Ensure rebuilds, never a binary nobody checked.
	if err := writeDlvManifest(dir, dlvManifest{Version: t.Pin.Version, ModuleSum: t.Pin.ModuleSum, SHA256: sum, Built: time.Now().UTC()}); err != nil {
		return err
	}
	if err := os.Chmod(built, 0o755); err != nil { //nolint:gosec // G302: the app container runs it as the host uid
		return err
	}
	return os.Rename(built, filepath.Join(dir, "dlv"))
}

// dlvBuildEnv is the host environment with every setting that could relax
// module verification or redirect the build reset.
func dlvBuildEnv(base []string, scratch string) []string {
	reset := map[string]bool{
		"GOPROXY": true, "GOSUMDB": true, "GONOSUMDB": true, "GONOSUMCHECK": true, "GONOPROXY": true,
		"GOPRIVATE": true, "GOINSECURE": true, "GOFLAGS": true, "GOMODCACHE": true, "GOBIN": true,
		"GOPATH": true, "GOWORK": true, "GO111MODULE": true, "CGO_ENABLED": true, "GOOS": true,
		"GOARCH": true, "GOVCS": true, "GOENV": true,
	}
	env := make([]string, 0, len(base)+16)
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !reset[k] {
			env = append(env, kv)
		}
	}
	return append(env,
		"GOPROXY=https://proxy.golang.org", "GOSUMDB=sum.golang.org",
		"GONOSUMDB=", "GONOSUMCHECK=", "GONOPROXY=", "GOPRIVATE=", "GOINSECURE=",
		"GOFLAGS=-modcacherw", "GOENV=off", "GOWORK=off", "GO111MODULE=on", "CGO_ENABLED=0",
		"GOOS=linux", "GOARCH="+runtime.GOARCH,
		"GOPATH="+filepath.Join(scratch, "gopath"),
		"GOMODCACHE="+filepath.Join(scratch, "mod"),
		"GOBIN="+filepath.Join(scratch, "bin"),
	)
}

// goCandidates are where serve looks for go when PATH has none: a systemd
// unit's PATH is short.
var goCandidates = []string{"/usr/local/go/bin/go", "/usr/lib/go/bin/go", "~/.local/go/bin/go", "~/go/bin/go", "~/sdk/go/bin/go"}

// FindGo returns the go command serve builds Delve with.
func FindGo() (string, error) {
	if p, err := exec.LookPath("go"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	for _, c := range goCandidates {
		if rest, ok := strings.CutPrefix(c, "~/"); ok {
			if home == "" {
				continue
			}
			c = filepath.Join(home, rest)
		}
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("debug runs build Delve on the host and need Go: install Go (1.25 or newer) on this host")
}

// ExecGo runs the host's go command.
func ExecGo(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	bin, err := FindGo()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // G204: fixed go subcommands with a pinned module
	cmd.Dir, cmd.Env = dir, env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("%w: %s", err, tailLines(stderr.String(), errorTailLines))
	}
	return stdout.Bytes(), nil
}

func readDlvManifest(dir string) (dlvManifest, error) {
	var m dlvManifest
	b, err := os.ReadFile(filepath.Join(dir, dlvManifestName)) //nolint:gosec // G304: serve's own tools directory
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}

func writeDlvManifest(dir string, m dlvManifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, dlvManifestName+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, dlvManifestName))
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: serve's own tools directory
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
