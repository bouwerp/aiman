package tools

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Pinned tools. A pin is a release version and the sha256 of its archive.
//
//   - Node: the official linux tarball (.tar.gz) of the 24 LTS line. Node
//     26 links libatomic.so.1, which minimal glibc images (the adapter's
//     Debian slim among them) do not ship; 24 needs only libc, libstdc++
//     and libgcc. The sums are the release's SHASUMS256.txt lines
//     (https://nodejs.org/dist/v24.22.3/SHASUMS256.txt), copied here so a
//     changed file on the mirror is refused.
//   - js-debug: the standalone DAP server, GitHub release asset
//     js-debug-dap-<v>.tar.gz of microsoft/vscode-js-debug. The release has
//     no checksum file; the sum is the asset digest GitHub's release API
//     lists (assets[].digest), which matches a download's sha256.
//   - Bun: the official bun-linux-<arch>.zip of oven-sh/bun, release tag
//     bun-<version>. The sums are the release's SHASUMS256.txt lines.
//   - bun-dap: Bun's own debug adapter packages at the same Bun tag plus
//     aiman's DAP-over-TCP entry (build/bun-dap), bundled by that Bun into
//     one file. The build is reproducible: build/bun-dap/build.sh prints
//     BunDAPSHA256, CI checks it, and every aiman release carries the file
//     as the asset bun-dap-<Bun version>.js, listed in its checksums.txt.
//   - debugpy: the pure-Python wheel debugpy-<v>-py2.py3-none-any.whl from
//     PyPI. The sum is the one PyPI lists for the file
//     (https://pypi.org/pypi/debugpy/1.8.22/json, urls[].digests.sha256).
const (
	NodeVersion    = "v24.22.3"
	JSDebugVersion = "v1.140.1"
	JSDebugSHA256  = "c72fab31bcbfdce6ad1f42adfd28eff8d4681dd3b03b5e2e6f9e72e6f9927faf"
	BunVersion     = "v1.3.14"
	BunDAPSHA256   = "4f3db5296fee2b9b1b8e0d4e460ab8a6b7b2eae17da95ecfb772b378250708b0"
	// BunDAPFile is the adapter's file name in its tool directory.
	BunDAPFile = "bun-dap.js"
	// DebugpyVersion and DebugpySHA256 pin the debugpy wheel.
	DebugpyVersion = "v1.8.22"
	DebugpySHA256  = "a9e9d3550e15ca479c59333e90845029190531f0cacfedab3b815a57bd913947"
	// debugpyURL is the pinned wheel on PyPI's file host.
	debugpyURL = "https://files.pythonhosted.org/packages/56/3d/c7dc9f35bc9e22cdb53679f844bee40fd5cff871862f60595869a433400d/debugpy-1.8.22-py2.py3-none-any.whl"
	// aimanReleases is where aiman's release assets download from.
	aimanReleases = "https://github.com/bouwerp/aiman/releases"
)

// bunSHA256 is the pinned Bun zip per GOARCH.
var bunSHA256 = map[string]string{
	"amd64": "951ee2aee855f08595aeec6225226a298d3fea83a3dcd6465c09cbccdf7e848f",
	"arm64": "a27ffb63a8310375836e0d6f668ae17fa8d8d18b88c37c821c65331973a19a3b",
}

// bunArch is Bun's name for a GOARCH.
var bunArch = map[string]string{"amd64": "x64", "arm64": "aarch64"}

// nodeSHA256 is the pinned Node tarball per GOARCH.
var nodeSHA256 = map[string]string{
	"amd64": "061ec3e3062115705b56b51fa9cc330ab50d34b6969387d4e4b1a50c6809acc2",
	"arm64": "261af2af250dfd7e9f25cf151d1b2713a0adf8f10a9ad116c4947f68627affe8",
}

// nodeArch is Node's name for a GOARCH.
var nodeArch = map[string]string{"amd64": "x64", "arm64": "arm64"}

// Pin overrides a tool's version from config (harness.debug_tools); it needs
// the archive's sha256 for this host's architecture. URL, when set, is where
// the archive comes from instead: an https URL or an absolute path on the
// host (a local build of bun-dap, for example). The zero Pin is the version
// in code.
type Pin struct {
	Version string `yaml:"version" json:"version"`
	SHA256  string `yaml:"sha256" json:"sha256"`
	URL     string `yaml:"url" json:"url"`
}

var (
	versionRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	sha256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// url is the pin's own source, else def.
func (p Pin) url(tool, def string) (string, error) {
	switch {
	case p.URL == "":
		return def, nil
	case strings.HasPrefix(p.URL, "https://") && !strings.ContainsAny(p.URL, " \n"):
		return p.URL, nil
	case filepath.IsAbs(p.URL) && filepath.Clean(p.URL) == p.URL:
		return p.URL, nil
	}
	return "", fmt.Errorf("harness.debug_tools.%s.url %q must be an https URL or a clean absolute path", tool, p.URL)
}

func (p Pin) resolve(tool, version, sum string) (string, string, error) {
	if p.Version == "" && p.SHA256 == "" {
		if p.URL != "" {
			return "", "", fmt.Errorf("harness.debug_tools.%s.url needs version and sha256", tool)
		}
		return version, sum, nil
	}
	if !versionRe.MatchString(p.Version) {
		return "", "", fmt.Errorf("harness.debug_tools.%s.version %q must be vX.Y.Z", tool, p.Version)
	}
	if p.Version == version && (p.SHA256 == "" || p.SHA256 == sum) {
		return version, sum, nil
	}
	s := strings.ToLower(p.SHA256)
	if !sha256Re.MatchString(s) {
		return "", "", fmt.Errorf("harness.debug_tools.%s %s needs sha256, the archive's hex sha256 for this host's architecture", tool, p.Version)
	}
	return p.Version, s, nil
}

// Node is the Node runtime for arch: bin/node only, which runs js-debug in
// the adapter container and, when the app image has none, the app.
func Node(arch string, p Pin) (Spec, error) {
	na, ok := nodeArch[arch]
	if !ok {
		return Spec{}, fmt.Errorf("node: no pinned build for %s", arch)
	}
	version, sum, err := p.resolve("node", NodeVersion, nodeSHA256[arch])
	if err != nil {
		return Spec{}, err
	}
	url, err := p.url("node", fmt.Sprintf("https://nodejs.org/dist/%s/node-%s-linux-%s.tar.gz", version, version, na))
	if err != nil {
		return Spec{}, err
	}
	return Spec{
		Name: "node", Version: version, SHA256: sum, Strip: 1, URL: url,
		Keep: func(rel string) bool { return rel == "bin/node" || rel == "LICENSE" },
	}, nil
}

// JSDebug is js-debug's standalone DAP server (src/dapDebugServer.js). Its
// Windows-only native modules are left out.
func JSDebug(p Pin) (Spec, error) {
	version, sum, err := p.resolve("js-debug", JSDebugVersion, JSDebugSHA256)
	if err != nil {
		return Spec{}, err
	}
	url, err := p.url("js-debug", fmt.Sprintf("https://github.com/microsoft/vscode-js-debug/releases/download/%s/js-debug-dap-%s.tar.gz", version, version))
	if err != nil {
		return Spec{}, err
	}
	return Spec{
		Name: "js-debug", Version: version, SHA256: sum, Strip: 1, URL: url,
		Keep: func(rel string) bool { return !strings.HasSuffix(rel, ".node") },
	}, nil
}

// Bun is the Bun runtime for arch: the bun binary only. It runs bun-dap in
// the adapter container and, when the app image has none, the app.
func Bun(arch string, p Pin) (Spec, error) {
	ba, ok := bunArch[arch]
	if !ok {
		return Spec{}, fmt.Errorf("bun: no pinned build for %s", arch)
	}
	version, sum, err := p.resolve("bun", BunVersion, bunSHA256[arch])
	if err != nil {
		return Spec{}, err
	}
	url, err := p.url("bun", fmt.Sprintf("https://github.com/oven-sh/bun/releases/download/bun-%s/bun-linux-%s.zip", version, ba))
	if err != nil {
		return Spec{}, err
	}
	return Spec{
		Name: "bun", Version: version, SHA256: sum, Strip: 1, URL: url, Format: FormatZip,
		Keep: func(rel string) bool { return rel == "bun" },
	}, nil
}

// BunDAPAsset is the release asset name of the bun-dap build for a Bun
// version.
func BunDAPAsset(bunVersion string) string { return "bun-dap-" + bunVersion + ".js" }

// BunDAP is Bun's debug adapter built for the Bun version of the pin (by
// default BunVersion). It downloads from the aiman release named release
// (serve's own version) and from the latest release when release is not a
// vX.Y.Z tag (a dev build); the sha256 in code is what makes either safe.
// A dev build whose build/bun-dap changed builds the file with
// build/bun-dap/build.sh and names it in harness.debug_tools.bun-dap.
func BunDAP(release string, p Pin) (Spec, error) {
	version, sum, err := p.resolve("bun-dap", BunVersion, BunDAPSHA256)
	if err != nil {
		return Spec{}, err
	}
	def := aimanReleases + "/latest/download/" + BunDAPAsset(version)
	if versionRe.MatchString(release) {
		def = aimanReleases + "/download/" + release + "/" + BunDAPAsset(version)
	}
	url, err := p.url("bun-dap", def)
	if err != nil {
		return Spec{}, err
	}
	return Spec{
		Name: "bun-dap", Version: version, SHA256: sum, URL: url, Format: FormatFile, File: BunDAPFile,
		Hint: "a build other than this release's needs build/bun-dap/build.sh and harness.debug_tools.bun-dap {version, sha256, url}",
	}, nil
}

// Debugpy is the debugpy pure-Python wheel (py2.py3-none-any), unpacked at
// the tool directory's root so the directory goes on PYTHONPATH as it is.
// Its native helpers (attach by pid, Windows Cython builds) are left out: a
// harness run starts the target under debugpy and never attaches to a
// running pid. A version other than the pin needs the wheel's URL too,
// because PyPI's file URLs carry a hash path.
func Debugpy(p Pin) (Spec, error) {
	version, sum, err := p.resolve("debugpy", DebugpyVersion, DebugpySHA256)
	if err != nil {
		return Spec{}, err
	}
	def := debugpyURL
	if version != DebugpyVersion {
		if p.URL == "" {
			return Spec{}, fmt.Errorf("harness.debug_tools.debugpy %s needs url, the wheel's files.pythonhosted.org URL listed at https://pypi.org/project/debugpy/%s/#files", version, strings.TrimPrefix(version, "v"))
		}
		def = ""
	}
	url, err := p.url("debugpy", def)
	if err != nil {
		return Spec{}, err
	}
	return Spec{
		Name: "debugpy", Version: version, SHA256: sum, URL: url, Format: FormatZip,
		Keep: keepDebugpy,
	}, nil
}

// keepDebugpy keeps the wheel's Python package and its license, without
// native binaries.
func keepDebugpy(rel string) bool {
	if !strings.HasPrefix(rel, "debugpy/") {
		return strings.HasSuffix(rel, ".dist-info/licenses/LICENSE")
	}
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".so", ".dll", ".dylib", ".exe", ".pyd", ".pdb":
		return false
	}
	return true
}
