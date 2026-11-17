package harness

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

const (
	testNodeDir    = "/home/u/.aiman/harness/tools/node-v24.22.3-linux-amd64"
	testJSDebugDir = "/home/u/.aiman/harness/tools/js-debug-v1.140.1-linux-amd64"
	nodeDebugBlock = "node, entry: dist/server.js, cwd: api, runtime_args: [--enable-source-maps]"
)

func nodeSpec(t *testing.T, fileYAML string) RunSpec {
	t.Helper()
	f, err := Parse([]byte(fileYAML))
	if err != nil {
		t.Fatal(err)
	}
	ds, err := NodeDebugSpec(testNodeDir, testJSDebugDir, f)
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec()
	spec.Debug = ds
	spec.DebugSubnet = netip.MustParsePrefix("10.232.1.0/29")
	spec.AdapterImage = "sha256:adapter"
	return spec
}

func TestAdapterOnlyOnDebugNetwork(t *testing.T) {
	file := debugFixture(nodeDebugBlock)
	p, doc := buildDebug(t, file, nodeSpec(t, file))

	ad := svcOf(t, doc, ServiceAdapter)
	nets, _ := ad["networks"].(map[string]any)
	if len(nets) != 1 {
		t.Fatalf("adapter networks %v, want the debug network only", nets)
	}
	if dn, _ := nets[NetworkDebug].(map[string]any); dn["ipv4_address"] != "10.232.1.3" {
		t.Fatalf("adapter debug attach %v, want 10.232.1.3", nets[NetworkDebug])
	}
	// The app profile: read-only root, host uid, no capabilities, no new
	// privileges, small limits, nothing published.
	if ad["image"] != "sha256:adapter" || ad["pull_policy"] != "never" || ad["read_only"] != true || ad["user"] != "1004:1004" {
		t.Fatalf("adapter = %v", ad)
	}
	if !slices.Equal(strs(ad["cap_drop"]), []string{"ALL"}) || !slices.Contains(strs(ad["security_opt"]), "no-new-privileges:true") {
		t.Fatalf("adapter hardening = %v %v", ad["cap_drop"], ad["security_opt"])
	}
	if ad["mem_limit"] != 512<<20 || ad["cpus"] != 1 || ad["pids_limit"] != 128 || ad["ports"] != nil {
		t.Fatalf("adapter limits = %v %v %v ports %v", ad["mem_limit"], ad["cpus"], ad["pids_limit"], ad["ports"])
	}
	vols := strs(ad["volumes"])
	for _, want := range []string{
		testNodeDir + ":" + NodeToolMount + ":ro", testJSDebugDir + ":" + JSDebugMount + ":ro",
		testSpec().Src + ":/src:ro", testSpec().Out + ":/out:ro",
	} {
		if !slices.Contains(vols, want) {
			t.Fatalf("adapter volumes %v lack %s", vols, want)
		}
	}
	cmd := strings.Join(strs(ad["command"]), " ")
	if !strings.Contains(cmd, JSDebugMount+"/src/dapDebugServer.js 8123") || !strings.Contains(cmd, "$$"+AdapterHostEnv) {
		t.Fatalf("adapter command %q", cmd)
	}
	if env, _ := ad["environment"].(map[string]any); env[AdapterHostEnv] != "10.232.1.3" {
		t.Fatalf("adapter env %v", ad["environment"])
	}
	// Services stay on the inner network; only app, adapter and edge are on
	// the debug network.
	var onDebug []string
	for name := range doc["services"].(map[string]any) {
		if slices.Contains(networksOf(t, svcOf(t, doc, name)), NetworkDebug) {
			onDebug = append(onDebug, name)
		}
	}
	slices.Sort(onDebug)
	if !slices.Equal(onDebug, []string{ServiceApp, ServiceAdapter, ServiceEdge}) {
		t.Fatalf("on the debug network: %v", onDebug)
	}
	// The edge relays DAP to the adapter, not to the app's inspector.
	var dap Relay
	for _, r := range p.Relays {
		if r.Kind == RelayKindDAP {
			dap = r
		}
	}
	if dap.Target != "10.232.1.3:8123" || dap.Service != ServiceAdapter {
		t.Fatalf("dap relay = %+v", dap)
	}
	// A debug run with an adapter needs its image.
	f, _ := Parse([]byte(file))
	spec := nodeSpec(t, file)
	spec.AdapterImage = ""
	if _, err := Compose(f, spec); err == nil {
		t.Fatal("an adapter without an image was accepted")
	}
}

func TestInspectorBindsDebugAddress(t *testing.T) {
	file := debugFixture(nodeDebugBlock)
	_, doc := buildDebug(t, file, nodeSpec(t, file))
	app := svcOf(t, doc, ServiceApp)
	env, _ := app["environment"].(map[string]any)
	if env[InspectEnv] != "10.232.1.2:9229" {
		t.Fatalf("app %s = %v, want the app's debug address", InspectEnv, env[InspectEnv])
	}
	cmd := strings.Join(strs(app["command"]), " ")
	for _, want := range []string{
		`--inspect-wait="$$AIMAN_INSPECT"`, "'--enable-source-maps' '/src/dist/server.js'",
		"cd '/src/api'", NodeToolMount + "/bin",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("app command %q lacks %q", cmd, want)
		}
	}
	for _, bad := range []string{"0.0.0.0", "--watch", "--inspect=", "--inspect-brk"} {
		if strings.Contains(cmd, bad) {
			t.Fatalf("app command %q holds %q", cmd, bad)
		}
	}
	vols := strs(app["volumes"])
	if !slices.Contains(vols, testNodeDir+":"+NodeToolMount+":ro") || slices.ContainsFunc(vols, func(v string) bool { return strings.Contains(v, "js-debug") }) {
		t.Fatalf("app volumes %v: want node read-only and no js-debug", vols)
	}

	// A whole debug command runs as written, in cwd.
	run := debugFixture("node, cwd: api, run: 'node --import tsx --inspect-wait=$AIMAN_INSPECT src/index.ts'")
	_, doc = buildDebug(t, run, nodeSpec(t, run))
	cmd = strings.Join(strs(svcOf(t, doc, ServiceApp)["command"]), " ")
	if !strings.Contains(cmd, "node --import tsx --inspect-wait=$$AIMAN_INSPECT src/index.ts") || !strings.Contains(cmd, "cd '/src/api'") {
		t.Fatalf("app command %q", cmd)
	}
}

func TestNodeLaunchAttaches(t *testing.T) {
	f, err := Parse([]byte(debugFixture(nodeDebugBlock)))
	if err != nil {
		t.Fatal(err)
	}
	app, adapter, edge := DebugAddrs(netip.MustParsePrefix("10.232.7.0/29"))
	l, err := (&NodeDebug{}).Launch(f, DebugNet{App: app, Adapter: adapter, Edge: edge}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := l.Arguments
	if l.Request != "attach" || !l.Children || l.SourceRoot != "/src" || l.Lang != "node" {
		t.Fatalf("launch = %+v", l)
	}
	if a["type"] != "pwa-node" || a["request"] != "attach" || a["address"] != "10.232.7.2" || a["port"] != 9229 || a["cwd"] != "/src/api" {
		t.Fatalf("attach = %v", a)
	}
	if a["sourceMaps"] != true || !slices.Contains(a["outFiles"].([]string), "/out/**/*.(m|c|)js") {
		t.Fatalf("source maps = %v %v", a["sourceMaps"], a["outFiles"])
	}

	cfg := (&NodeDebug{}).EditorConfig(l, 4711)
	if cfg["debugServer"] != 4711 || cfg["type"] != "pwa-node" || cfg["request"] != "attach" || cfg["address"] != "10.232.7.2" {
		t.Fatalf("editor = %v", cfg)
	}
	if cfg[LocalRootKey] != "${workspaceFolder}" {
		t.Fatalf("editor %s = %v", LocalRootKey, cfg[LocalRootKey])
	}
}

func TestNodeCheckRefuses(t *testing.T) {
	// A runtime path that is not under /src, or a loader as runtime, never
	// reaches compose: the parser refuses it; the adapter takes every
	// parsed node block.
	for _, block := range []string{nodeDebugBlock, "node, run: 'node --inspect-wait=$AIMAN_INSPECT x.js'"} {
		f, err := Parse([]byte(debugFixture(block)))
		if err != nil {
			t.Fatal(err)
		}
		if err := (&NodeDebug{}).Check(f); err != nil {
			t.Fatalf("%s: %v", block, err)
		}
	}
	f, _ := Parse([]byte(debugFixture("go")))
	if err := (&NodeDebug{}).Check(f); err == nil {
		t.Fatal("node checked a go block")
	}
}
