package harnesstest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/tools"
)

// VitestBreakLine is the line of the expect inside the test in VitestTest.
const VitestBreakLine = 6

// VitestTest is the Vitest 4 test a debug check runs; VitestSum the module
// it tests.
const VitestTest = `import { describe, expect, it } from "vitest";
import { sum } from "../src/sum";

describe("sum", () => {
  it("adds", () => {
    const total: number = sum(2, 3);
    expect(total).toBe(5);
  });
});
`

// VitestSum is src/sum.ts.
const VitestSum = `export function sum(a: number, b: number): number {
  return a + b;
}
`

// VitestCheckFile is a node app (the compiled TypeScript of noderun.go)
// under its debugger, with a Vitest check that debugs under Node.
const VitestCheckFile = `version: 1
app:
  run: node dist/app.js
  debug: {lang: node, entry: dist/app.js}
scenarios:
  - id: dbg
    checks:
      - name: sum
        run: node node_modules/vitest/vitest.mjs run tests/sum.test.ts
        debug:
          lang: node
          run: node node_modules/vitest/vitest.mjs run --inspect-brk=$AIMAN_INSPECT --no-file-parallelism --test-timeout=0 tests/sum.test.ts
`

// npmPin is one npm package of the Vitest fixture: the registry tarball
// and its sha256, taken from the registry once (npm verified each against
// its sha512 integrity) and pinned here.
type npmPin struct{ name, version, sha256 string }

// vitestPins are vitest 4.1.12 and everything it installs on linux-x64
// (npm install --ignore-scripts, 2026-11-17): Vite 8 with rolldown and
// lightningcss for glibc x64.
var vitestPins = []npmPin{
	{"@jridgewell/sourcemap-codec", "1.6.0", "f30a35f6af8c347d01b8ff2f8662272f7d986cf21b916704d86c249a90b97e25"},
	{"@oxc-project/types", "0.156.0", "9ea3e4ff27f4c9c7bc9b5a8feb7b7d4f2181dd2b1d90067d52f267b6c5897dbc"},
	{"@rolldown/binding-linux-x64-gnu", "1.2.19", "0e190f8d423a92ec707b65712372556f6740db159491e2df1da89d012f71d83e"},
	{"@rolldown/pluginutils", "1.1.1", "0c610c7c5465637d8a95c4c1f8472269ee9e0d2d8db18dcb1cc427d33af7e40d"},
	{"@standard-schema/spec", "1.1.0", "a7cb7268be280ab518d450f8c3b07c86f23417225c0ab53691ed59b3067cceaf"},
	{"@types/chai", "5.2.3", "5f0c52e34238e17d2d9a2d6e9b85530765afae645f265e0e14263ebeecdd7f94"},
	{"@types/deep-eql", "4.0.2", "b63d185ff2cd2223bd6962b12b10342df4c5a4c25fd20017d2d9f335c35f3607"},
	{"@types/estree", "1.0.10", "1af423ac6ca779d55b6725c3573933337e3f6cba97a1ca18094766a9afa351f3"},
	{"@vitest/expect", "4.1.12", "8c932064dafe90dca40312b44c5b63623c8c7e03bb9b38c4bd9af2f5701f7bb6"},
	{"@vitest/mocker", "4.1.12", "8f9ac0144028329f5c649c031608b8877efea748cdbded0d7f41d60f9be49c52"},
	{"@vitest/pretty-format", "4.1.12", "929beb93dafed9e676e55f07919839643231679384020a5ef9c2d02022a5dd80"},
	{"@vitest/runner", "4.1.12", "061db956dceb2b291e1a4ceb3c5b7e31c368e75b34411953402e339ef566e6cf"},
	{"@vitest/snapshot", "4.1.12", "10b57fb1cb37d110309dd64e22a154bf26bd98526cc1ea5307b2f2478cbdddf5"},
	{"@vitest/spy", "4.1.12", "eb9a67720d1082f33bfc5cf3a41ee6d297e994cf26013dae0ad807a474fa44a3"},
	{"@vitest/utils", "4.1.12", "0c25f072d4afcb52fa48266177111ca90bc4116c66473170959aae1d57db347d"},
	{"assertion-error", "2.0.1", "73f4a5b3c9b38e4f3123188d4d85a7933ee5fe2f77f8ff04e8867b0938c20f56"},
	{"chai", "6.3.0", "ef5457b2ab9e011093f47f104fecc94e5b36ce729fdba6fbc8b0ce316c2e8f60"},
	{"convert-source-map", "2.0.0", "17f7253ec7e4e09f68371192fded2b5241a5cf85e610e5059b1d49e878fe1898"},
	{"detect-libc", "2.1.2", "270dec0fc06cff86481da8af2dd8f18dee6b602790b14ef0e1c2c18d7da39427"},
	{"es-module-lexer", "2.3.2", "99b4da5f3bd7cc4483f3d892ee9037b10a1da26ac5bec374038cf0bd4a52e1a7"},
	{"estree-walker", "3.0.3", "bbbdfdc16cd9ef099e474f55a4aa344103c01853727136a76ee165fa9d1c72b3"},
	{"expect-type", "1.4.0", "c44b3fea7bcae8a4ce328b457da171e8debf553cbeb6a049817975b8e050e3a1"},
	{"fdir", "6.5.0", "241ddcd9f31e2a1fca405326017dbb8333ce174198e887f0f96387c29f0d365e"},
	{"lightningcss", "1.33.0", "d492f4278bb7234ceaa3c2638ef944e108ea17a23db7c6a2367c14fa725598cf"},
	{"lightningcss-linux-x64-gnu", "1.33.0", "fd485dfd57082c4ef864dd867fa87fd8e1a9b5d58ee85c2b76e28e42c8f3e1d3"},
	{"magic-string", "0.30.21", "fa49762567dca44e8ccf327f365d250e04c482bf7a4ed0ae7d03659b09ac656f"},
	{"nanoid", "3.3.20", "5ae134779aa22c2c04b691d361bc1f3c004c354e81c7ee166ea1c6e848eedc4f"},
	{"obug", "2.2.1", "cbf042980f79918d07c46817fe18ed0db6a8e55893dd7347b7cad71e3e5f65a7"},
	{"pathe", "2.0.3", "6e3b73bc5dbb7f8a108ff5d8459b07da04062e822c662339416525510696df36"},
	{"picocolors", "1.1.1", "d3aedb2807967b7eb37fd11b03b7e3701e725af79c650d0812ff7213f8f882d9"},
	{"picomatch", "4.0.7", "697b463be3cdbd9ffc8f4194fd5019a69ddf95875fb703d78b2b115cc9e740a8"},
	{"postcss", "8.5.33", "ed476390bb68df554790d035a942608a4d6ec0ec65147f9eaef182a24d815a8f"},
	{"rolldown", "1.2.19", "f9a8f5065c938c361c9cc5cabc8da4e378fdd13d36bd63c56892a0fa2da9d43c"},
	{"siginfo", "2.0.0", "a20fd853c4514049964aca176d4198d58802b28dc481e82cb57f2101b2c7701b"},
	{"source-map-js", "1.2.2", "05c8cf8e7c3a6b56cada7668fe9342b10f4b625efc92aba9ba05b9e8fe4d71a3"},
	{"stackback", "0.0.2", "3e663c82d1ccf9748ed446b648ec21a7dd6c5895ecf4a95310a60a320ca55858"},
	{"std-env", "4.3.0", "472694210ae3770970a33826b283db03ecec5a0fcf3f6df76eccb48695459bf5"},
	{"tinybench", "2.9.0", "3a38e953c2366359670fb41e7efae2d8a818ac12615fbea6f9d0614010582e39"},
	{"tinyexec", "1.3.1", "540b804f39c7716c232252085e4d43d9b10e2d89db39faaa1b31ed8cd93e2f1f"},
	{"tinyglobby", "0.2.17", "afb4afae9175f8b873ac13e1d21cea0997a0bcb40c3598613a834490058cb562"},
	{"tinyrainbow", "3.2.0", "97d53ca3a53834d45c68bd6ec2c8a64cc2e3282d744c8dc4ee0ed7e6e2913636"},
	{"vite", "8.3.8", "96b14ea41f889b70b1ccf74dd5861e365816fce8632c5073ce8d70c90509738b"},
	{"vitest", "4.1.12", "ac79c9561c99eabc1cc29e0a839cbd03d1ff0e45bbecb5a2edfa6c3827c823d1"},
	{"why-is-node-running", "2.3.0", "d2f668f77953509d0079ef05617ac9f5b5f64ed194f8b05872ad12ba06ec9963"},
}

// spec is the pin as a tools store download from the npm registry.
func (p npmPin) spec() tools.Spec {
	base := p.name[strings.LastIndex(p.name, "/")+1:]
	return tools.Spec{
		Name:    "npm-" + strings.NewReplacer("@", "", "/", "-").Replace(p.name),
		Version: "v" + p.version,
		URL:     "https://registry.npmjs.org/" + p.name + "/-/" + base + "-" + p.version + ".tgz",
		SHA256:  p.sha256,
		Strip:   1,
	}
}

var (
	vitestOnce sync.Once
	vitestDirs map[string]string
	vitestErr  error
)

// vitestPackages fetches every pinned package through the tools store,
// which checks its sha256 before it unpacks, and caches them in the user
// cache. Nothing is installed in a container.
func vitestPackages(t *testing.T) map[string]string {
	t.Helper()
	if runtime.GOARCH != "amd64" {
		t.Skip("the Vitest fixture pins linux-x64 native packages")
	}
	vitestOnce.Do(func() {
		dir, err := cacheDir()
		if err != nil {
			vitestErr = err
			return
		}
		store := &tools.Store{Dir: filepath.Join(dir, "tools")}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		vitestDirs = map[string]string{}
		for _, p := range vitestPins {
			d, err := store.Ensure(ctx, p.spec())
			if err != nil {
				vitestErr = err
				return
			}
			vitestDirs[p.name] = d
		}
	})
	if vitestErr != nil {
		t.Fatal(vitestErr)
	}
	return vitestDirs
}

// VitestRepo is a git repository holding the node app, the Vitest test and
// its node_modules, committed so a harness run exports it.
func VitestRepo(t *testing.T) string {
	t.Helper()
	pkgs := vitestPackages(t)
	repo := filepath.Join(t.TempDir(), "repo")
	files := map[string]string{
		".aiman/harness.yaml": VitestCheckFile,
		"package.json":        `{"name":"fixture","private":true,"type":"module"}` + "\n",
		"src/app.ts":          TSSource,
		"dist/app.js":         TSCompiled,
		"dist/app.js.map":     TSMap,
		"src/sum.ts":          VitestSum,
		"tests/sum.test.ts":   VitestTest,
	}
	for rel, body := range files {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, d := range pkgs {
		if err := copyTree(d, filepath.Join(repo, "node_modules", filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=T", "-c", "user.email=t@example.com", "add", "-A"},
		{"-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q", "-m", "fixture"},
	} {
		cmd := exec.Command("git", args...) //nolint:gosec // G204: fixed git arguments
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return repo
}

// NewVitestManager is a harness manager on the host's Docker with the Node
// adapter on the pinned Node and js-debug, and no image vetting: the app
// runs in aiman's adapter image, which has no node of its own, so both the
// app and the debug check run on the mounted Node.
func NewVitestManager(t *testing.T, attach time.Duration) *harness.Manager {
	t.Helper()
	NeedDocker(t)
	for _, img := range []string{harness.EdgeImage, harness.AdapterImage} {
		ensureImage(t, img)
	}
	nodeTools(t)
	dir, err := cacheDir()
	if err != nil {
		t.Fatal(err)
	}
	m, err := harness.NewManager(harness.Options{
		Dir:      filepath.Join(t.TempDir(), "harness"),
		AimanBin: edgeBinary(t),
		UID:      os.Getuid(), GID: os.Getgid(),
		AllowedImages:      []string{harness.AdapterImage},
		Docker:             harness.ExecDocker{},
		AppImage:           func(string, string) (string, error) { return harness.AdapterImage, nil },
		DebugAdapters:      []harness.DebugAdapter{&harness.NodeDebug{Tools: &tools.Store{Dir: filepath.Join(dir, "tools")}}},
		DebugAttachTimeout: attach,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}
