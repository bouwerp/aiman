package tools

import (
	"fmt"
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
const (
	NodeVersion    = "v24.22.3"
	JSDebugVersion = "v1.140.1"
	JSDebugSHA256  = "c72fab31bcbfdce6ad1f42adfd28eff8d4681dd3b03b5e2e6f9e72e6f9927faf"
)

// nodeSHA256 is the pinned Node tarball per GOARCH.
var nodeSHA256 = map[string]string{
	"amd64": "061ec3e3062115705b56b51fa9cc330ab50d34b6969387d4e4b1a50c6809acc2",
	"arm64": "261af2af250dfd7e9f25cf151d1b2713a0adf8f10a9ad116c4947f68627affe8",
}

// nodeArch is Node's name for a GOARCH.
var nodeArch = map[string]string{"amd64": "x64", "arm64": "arm64"}

// Pin overrides a tool's version from config (harness.debug_tools); it needs
// the archive's sha256 for this host's architecture. The zero Pin is the
// version in code.
type Pin struct {
	Version string `yaml:"version" json:"version"`
	SHA256  string `yaml:"sha256" json:"sha256"`
}

var (
	versionRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	sha256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func (p Pin) resolve(tool, version, sum string) (string, string, error) {
	if p.Version == "" && p.SHA256 == "" {
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
	return Spec{
		Name: "node", Version: version, SHA256: sum, Strip: 1,
		URL:  fmt.Sprintf("https://nodejs.org/dist/%s/node-%s-linux-%s.tar.gz", version, version, na),
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
	return Spec{
		Name: "js-debug", Version: version, SHA256: sum, Strip: 1,
		URL:  fmt.Sprintf("https://github.com/microsoft/vscode-js-debug/releases/download/%s/js-debug-dap-%s.tar.gz", version, version),
		Keep: func(rel string) bool { return !strings.HasSuffix(rel, ".node") },
	}, nil
}
