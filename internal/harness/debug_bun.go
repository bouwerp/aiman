package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/bouwerp/aiman/internal/harness/tools"
	"github.com/bouwerp/aiman/internal/shellx"
)

// Bun and TypeScript on Bun. The app runs under Bun's inspector, bound to
// the app's debug address with a random per-run path
// ($AIMAN_INSPECT is <ip>:6499/<prefix>) and waiting for a debugger before
// it runs (--inspect-wait). Bun's own debug adapter (bun-dap.js: the
// bun-debug-adapter-protocol package of the pinned Bun tag behind a DAP TCP
// server, built by build/bun-dap) runs on the pinned Bun in the adapter
// container and attaches to that inspector over the WebKit Inspector
// Protocol; the edge relays DAP to it. Bun transpiles TypeScript itself and
// reports the .ts paths, so breakpoints land in the source under /src. The
// adapter speaks the inspector protocol of its Bun minor version, so the
// app's Bun must be of the same minor: Check refuses an app.image tag of
// another minor at harness.up, and the probe refuses the Bun the image
// really runs before the app starts.
const (
	// BunInspectPort is Bun's default inspector port, on the app's debug
	// address.
	BunInspectPort = 6499
	// BunDAPPort is bun-dap's DAP port on the adapter's address.
	BunDAPPort = 8124
	// BunToolMount and BunDAPMount are the tools' read-only mounts.
	BunToolMount = ToolsMount + "/bun"
	BunDAPMount  = ToolsMount + "/bun-dap"
	// bunPrefixBytes is the size of the inspector path's random prefix.
	bunPrefixBytes = 16
)

// BunDebug is the Bun adapter.
type BunDebug struct {
	// Tools keeps the pinned Bun and bun-dap on the host.
	Tools *tools.Store
	// Bun and DAP override the pins (harness.debug_tools.bun and bun-dap).
	Bun, DAP tools.Pin
	// Release is serve's aiman version: bun-dap downloads from that
	// release, or from the latest one for a dev build.
	Release string
}

// Lang is bun.
func (*BunDebug) Lang() string { return "bun" }

// Check accepts a bun block whose command can be built, unless app.image
// names a Bun of another minor version than the adapter's.
func (b *BunDebug) Check(f *File) error {
	if f.App.Debug == nil || f.App.Debug.Lang != "bun" {
		return invalid("the bun debugger needs app.debug with lang bun")
	}
	if _, err := bunScript(f.App.Debug); err != nil {
		return err
	}
	want, err := b.minor()
	if err != nil {
		return err
	}
	if got := imageBunMinor(f.App.Image); got != "" && got != want {
		return bunVersionError("app.image "+f.App.Image+" runs Bun "+got, want)
	}
	return nil
}

// minor is the Bun minor version bun-dap is built for.
func (b *BunDebug) minor() (string, error) {
	spec, err := tools.BunDAP(b.Release, b.DAP)
	if err != nil {
		return "", err
	}
	return bunMinor(spec.Version), nil
}

// AdapterImage is the pinned adapter container image.
func (*BunDebug) AdapterImage() string { return AdapterImage }

// Spec fetches or verifies Bun and bun-dap, places them, and picks the
// run's inspector prefix.
func (b *BunDebug) Spec(ctx context.Context, f *File) (*DebugSpec, error) {
	if b.Tools == nil {
		return nil, errors.New("the Bun debugger has no tools directory")
	}
	bunSpec, err := tools.Bun(runtime.GOARCH, b.Bun)
	if err != nil {
		return nil, err
	}
	dapSpec, err := tools.BunDAP(b.Release, b.DAP)
	if err != nil {
		return nil, err
	}
	bunDir, err := b.Tools.Ensure(ctx, bunSpec)
	if err != nil {
		return nil, toolError(err)
	}
	dapDir, err := b.Tools.Ensure(ctx, dapSpec)
	if err != nil {
		return nil, toolError(err)
	}
	prefix, err := NewBunPrefix()
	if err != nil {
		return nil, err
	}
	return BunDebugSpec(bunDir, dapDir, f, prefix, bunMinor(dapSpec.Version))
}

// NewBunPrefix is a random inspector path prefix: 16 bytes as hex. The
// inspector evaluates code in the app, so its URL is not guessable even on
// the debug network.
func NewBunPrefix() (string, error) {
	b := make([]byte, bunPrefixBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

var bunPrefixRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// BunDebugSpec runs the app under Bun's inspector at prefix with the
// host's Bun on PATH after the image's own, and bun-dap (built for Bun
// minor) on the host's Bun in the adapter container.
func BunDebugSpec(bunDir, dapDir string, f *File, prefix, minor string) (*DebugSpec, error) {
	d := f.App.Debug
	if d == nil {
		return nil, invalid("the bun debugger needs app.debug")
	}
	if !bunPrefixRe.MatchString(prefix) {
		return nil, fmt.Errorf("bun inspector prefix %q is not 32 hex digits", prefix)
	}
	script, err := bunScript(d)
	if err != nil {
		return nil, err
	}
	bun := ToolMount{Host: bunDir, Container: BunToolMount}
	return &DebugSpec{
		Lang:        "bun",
		Command:     []string{"sh", "-c", script},
		Mounts:      []ToolMount{bun},
		Port:        BunInspectPort,
		InspectPath: prefix,
		Probe: &DebugProbe{
			Command: []string{"sh", "-c", bunProbeScript(d)},
			Check:   func(out string) error { return CheckBunProbe(out, minor) },
		},
		Adapter: &AdapterSpec{
			Command: []string{"sh", "-c", "exec " + BunToolMount + "/bun " + BunDAPMount + "/" + tools.BunDAPFile +
				` --listen "$` + AdapterHostEnv + `:` + strconv.Itoa(BunDAPPort) + `"`},
			Mounts: []ToolMount{bun, {Host: dapDir, Container: BunDAPMount}},
			Port:   BunDAPPort,
		},
	}, nil
}

// bunScript is the app command of a bun debug run.
func bunScript(d *Debug) (string, error) {
	return inspectScript(d, "bun", BunToolMount, "Bun")
}

// Probe output lines: the runtime the app starts, its version, and
// whether the image is on musl libc.
const (
	probeBunPath    = "AIMAN_BUN_PATH="
	probeBunVersion = "AIMAN_BUN_VERSION="
	probeMusl       = "AIMAN_MUSL=1"
)

// bunProbeScript reports the Bun the app's command starts (app.debug.runtime,
// else bun, with the mounted Bun last on PATH, from app.debug.cwd), its
// version, and musl libc.
func bunProbeScript(d *Debug) string {
	rt := shellx.Quote(debugRuntime(d, "bun"))
	return `PATH="$PATH:` + BunToolMount + `"; export PATH
cd ` + shellx.Quote(path.Join("/src", d.Cwd)) + ` || exit 1
p=$(command -v ` + rt + `) || p=
echo "` + probeBunPath + `$p"
if [ -n "$p" ]; then echo "` + probeBunVersion + `$(` + rt + ` --version 2>&1 | head -n 1)"; fi
if ls /lib/ld-musl-* >/dev/null 2>&1; then echo ` + probeMusl + `; fi
`
}

var (
	bunVersionRe    = regexp.MustCompile(`^v?(\d+)\.(\d+)\.\d+`)
	imageTagMinorRe = regexp.MustCompile(`^v?(\d+)\.(\d+)(?:[.-]|$)`)
)

// CheckBunProbe reads the probe's output: the app's Bun must exist, must
// not be the mounted (glibc) Bun on a musl image, and must be of minor.
func CheckBunProbe(out, minor string) error {
	var bun, version string
	musl := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, probeBunPath):
			bun = strings.TrimPrefix(line, probeBunPath)
		case strings.HasPrefix(line, probeBunVersion):
			version = strings.TrimPrefix(line, probeBunVersion)
		case line == probeMusl:
			musl = true
		}
	}
	if bun == "" {
		return fmt.Errorf("the app container has no Bun to start the app under the debugger (probe output %q)", tailLines(out, 5))
	}
	if musl && bun == BunToolMount+"/bun" {
		return &Error{Code: CodeDebugUnsupported, Lang: "bun", Reason: ReasonMusl,
			Msg: "this image uses musl libc (Alpine) and has no bun of its own; the mounted Bun needs glibc. Use an image with bun installed (oven/bun:<version>-alpine), or a glibc image."}
	}
	m := bunVersionRe.FindStringSubmatch(version)
	if m == nil {
		return fmt.Errorf("%s --version answered %q, not a Bun version", bun, version)
	}
	if got := m[1] + "." + m[2]; got != minor {
		return bunVersionError("the app runs Bun "+strings.TrimPrefix(version, "v")+" ("+bun+")", minor)
	}
	return nil
}

// bunVersionError refuses an app whose Bun minor version the adapter is not
// built for, and names the fallback.
func bunVersionError(what, minor string) *Error {
	return &Error{Code: CodeDebugUnsupported, Lang: "bun", Reason: ReasonBunVersion, Msg: what +
		", but this host's Bun debug adapter (bun-dap) speaks the inspector protocol of Bun " + minor +
		" only, and an adapter for another minor version misreads it. Run the app on Bun " + minor +
		".x, or point harness.debug_tools.bun-dap at a build for its minor (build/bun-dap/build.sh). " +
		"The fallback is to debug the code under Node as a Vitest check (checks[].debug with lang node, piece P6d)."}
}

// bunMinor is the major.minor of a vX.Y.Z version.
func bunMinor(version string) string {
	if m := bunVersionRe.FindStringSubmatch(version); m != nil {
		return m[1] + "." + m[2]
	}
	return ""
}

// imageBunMinor is the Bun minor version an official Bun image tag names
// (oven/bun:1.3-slim, oven/bun:1.3.14-alpine), or empty when the image is
// another one or its tag names no version (latest, slim, a digest only).
func imageBunMinor(ref string) string {
	if ref == "" {
		return ""
	}
	name, _, _ := strings.Cut(ref, "@")
	slash := strings.LastIndex(name, "/")
	colon := strings.LastIndex(name, ":")
	if colon <= slash {
		return ""
	}
	repo, tag := name[:colon], name[colon+1:]
	if repo != "oven/bun" && !strings.HasSuffix(repo, "/oven/bun") {
		return ""
	}
	m := imageTagMinorRe.FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return m[1] + "." + m[2]
}

// Launch is bun-dap's attach to the app's inspector at the run's prefix.
func (*BunDebug) Launch(f *File, net DebugNet, spec *DebugSpec) (DebugLaunch, error) {
	d := f.App.Debug
	if d == nil {
		return DebugLaunch{}, invalid("the bun debugger needs app.debug")
	}
	if spec == nil || !bunPrefixRe.MatchString(spec.InspectPath) {
		return DebugLaunch{}, errors.New("the bun debugger has no inspector prefix for this run")
	}
	return DebugLaunch{
		Lang: "bun", Adapter: "bun-dap", Request: "attach", SourceRoot: "/src",
		Arguments: map[string]any{
			"name": "aiman harness", "type": "bun", "request": "attach",
			"url":         "ws://" + net.App.String() + ":" + strconv.Itoa(BunInspectPort) + "/" + spec.InspectPath,
			"stopOnEntry": false,
		},
	}, nil
}

// EditorConfig attaches VS Code (with the Bun extension, debug type bun) to
// bun-dap in the adapter through the console's editor port: debugServer
// points VS Code at it instead of the extension's own adapter, and
// LocalRootKey tells the console the workspace folder it maps to /src.
func (*BunDebug) EditorConfig(l DebugLaunch, port int) map[string]any {
	cfg := maps.Clone(l.Arguments)
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["name"] = "aiman harness (Bun)"
	cfg["debugServer"] = port
	cfg[LocalRootKey] = "${workspaceFolder}"
	return cfg
}
