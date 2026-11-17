package harness

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const testDlvPath = "/home/u/.aiman/harness/tools/dlv-v1.27.2-linux-amd64/dlv"

func debugSpec() RunSpec {
	spec := testSpec()
	spec.Debug = GoDebugSpec(testDlvPath)
	spec.DebugSubnet = netip.MustParsePrefix("10.232.1.0/29")
	return spec
}

func debugFixture(lang string) string {
	return strings.Replace(composeFixture, "  ports:", "  debug: {lang: "+lang+"}\n  ports:", 1)
}

func buildDebug(t *testing.T, fileYAML string, spec RunSpec) (*Project, map[string]any) {
	t.Helper()
	f, err := Parse([]byte(fileYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p, err := Compose(f, spec)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(p.YAML, &doc); err != nil {
		t.Fatalf("yaml: %v\n%s", err, p.YAML)
	}
	return p, doc
}

func TestDebugNetworkOnlyEdge(t *testing.T) {
	p, doc := buildDebug(t, debugFixture("go"), debugSpec())

	nets, _ := doc["networks"].(map[string]any)
	dn, ok := nets[NetworkDebug].(map[string]any)
	if !ok {
		t.Fatalf("no %s network in %v", NetworkDebug, keys(nets))
	}
	if dn["internal"] != true || dn["enable_ipv6"] != false {
		t.Fatalf("debug network internal=%v enable_ipv6=%v, want true and false", dn["internal"], dn["enable_ipv6"])
	}

	svcs, _ := doc["services"].(map[string]any)
	var onDebug []string
	for name := range svcs {
		if slices.Contains(networksOf(t, svcOf(t, doc, name)), NetworkDebug) {
			onDebug = append(onDebug, name)
		}
	}
	slices.Sort(onDebug)
	if !slices.Equal(onDebug, []string{ServiceApp, ServiceEdge}) {
		t.Fatalf("services on the debug network: %v, want only app and edge", onDebug)
	}
	for _, name := range []string{"db", "cache"} {
		if got := networksOf(t, svcOf(t, doc, name)); !slices.Equal(got, []string{NetworkInner}) {
			t.Fatalf("service %s networks %v, want inner only", name, got)
		}
	}

	app := svcOf(t, doc, ServiceApp)
	appNets, _ := app["networks"].(map[string]any)
	dbg, _ := appNets[NetworkDebug].(map[string]any)
	if dbg["ipv4_address"] != "10.232.1.2" {
		t.Fatalf("app debug address %v, want 10.232.1.2", dbg["ipv4_address"])
	}
	edgeNets, _ := svcOf(t, doc, ServiceEdge)["networks"].(map[string]any)
	if ed, _ := edgeNets[NetworkDebug].(map[string]any); ed["ipv4_address"] != "10.232.1.4" {
		t.Fatalf("edge debug attach %v, want 10.232.1.4", edgeNets[NetworkDebug])
	}

	cmd := strings.Join(strs(app["command"]), " ")
	for _, want := range []string{DlvMount + " dap", `--listen="$AIMAN_INSPECT"`} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("app command %q lacks %q", cmd, want)
		}
	}
	for _, bad := range []string{"accept-multiclient", "api-version", "0.0.0.0", "go install", "/out/app serve"} {
		if strings.Contains(cmd, bad) {
			t.Fatalf("app command %q holds %q", cmd, bad)
		}
	}
	if !slices.Contains(strs(app["volumes"]), testDlvPath+":"+DlvMount+":ro") {
		t.Fatalf("app volumes %v lack the read-only dlv mount", app["volumes"])
	}
	env, _ := app["environment"].(map[string]any)
	if env[InspectEnv] != "10.232.1.2:2345" {
		t.Fatalf("app %s = %v", InspectEnv, env[InspectEnv])
	}
	if env["GOFLAGS"] != "'-gcflags=all=-N -l'" {
		t.Fatalf("app GOFLAGS = %v", env["GOFLAGS"])
	}
	for _, name := range []string{"db", "cache", ServiceEdge, ServiceCacheInit} {
		if strings.Contains(strings.Join(strs(svcOf(t, doc, name)["volumes"]), " "), DlvMount) {
			t.Fatalf("%s mounts dlv", name)
		}
	}

	var dap *Relay
	for i := range p.Relays {
		if p.Relays[i].Kind == RelayKindDAP {
			dap = &p.Relays[i]
		}
	}
	if dap == nil || dap.Name != RelayDAP || dap.Target != "10.232.1.2:2345" || dap.Service != ServiceApp {
		t.Fatalf("dap relay %+v in %+v", dap, p.Relays)
	}
	edgeCmd := strings.Join(strs(svcOf(t, doc, ServiceEdge)["command"]), " ")
	if !strings.Contains(edgeCmd, "--dap-relay dap=") || strings.Contains(edgeCmd, "--relay dap=") {
		t.Fatalf("edge command %q does not declare the DAP relay as exclusive", edgeCmd)
	}
	if !strings.Contains(edgeCmd, "--inner "+debugSpec().DebugSubnet.String()) {
		t.Fatalf("edge command %q does not list the debug subnet as inner", edgeCmd)
	}
}

func TestDebugComposeRefusals(t *testing.T) {
	f, err := Parse([]byte(composeFixture))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Compose(f, debugSpec()); err == nil || !strings.Contains(err.Error(), "debug") {
		t.Fatalf("debug without app.debug: %v", err)
	}
	f, err = Parse([]byte(debugFixture("node")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Compose(f, debugSpec()); err == nil || !strings.Contains(err.Error(), "go") {
		t.Fatalf("node debug: %v", err)
	}
	f, err = Parse([]byte(debugFixture("go")))
	if err != nil {
		t.Fatal(err)
	}
	spec := debugSpec()
	spec.DebugSubnet = spec.InnerSubnet
	if _, err := Compose(f, spec); err == nil {
		t.Fatal("a debug subnet overlapping the inner one was accepted")
	}
	spec = debugSpec()
	spec.Debug.Mounts[0].Host = "relative/dlv"
	if _, err := Compose(f, spec); err == nil {
		t.Fatal("a relative dlv path was accepted")
	}
	// Without debug the project has no debug network and no DAP relay.
	p, doc := buildDebug(t, debugFixture("go"), testSpec())
	if nets, _ := doc["networks"].(map[string]any); nets[NetworkDebug] != nil {
		t.Fatal("a plain run has a debug network")
	}
	for _, r := range p.Relays {
		if r.Kind == RelayKindDAP {
			t.Fatal("a plain run has a DAP relay")
		}
	}
}
