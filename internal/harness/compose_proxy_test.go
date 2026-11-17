package harness

import (
	"slices"
	"strings"
	"testing"
)

func proxySpec(online bool) RunSpec {
	spec := testSpec()
	spec.Proxy = &ProxySpec{Online: online, BodyBytes: 4096, Stubs: true}
	if online {
		spec.Proxy.UpstreamRoots = "/etc/ssl/certs/ca-certificates.crt"
	}
	return spec
}

func envOf(t *testing.T, s map[string]any) map[string]string {
	t.Helper()
	raw, _ := s["environment"].(map[string]any)
	out := map[string]string{}
	for k, v := range raw {
		out[k], _ = v.(string)
	}
	return out
}

func TestNoProxyListsServices(t *testing.T) {
	_, doc := buildFixture(t, proxySpec(false))
	app := svcOf(t, doc, ServiceApp)
	env := envOf(t, app)
	noProxy := strings.Split(env["NO_PROXY"], ",")
	for _, want := range []string{"localhost", "127.0.0.1", "app", "edge", "db", "cache", "10.231.1.0/24"} {
		if !slices.Contains(noProxy, want) {
			t.Errorf("NO_PROXY %q misses %s", env["NO_PROXY"], want)
		}
	}
	if slices.Contains(noProxy, "unused") {
		t.Errorf("NO_PROXY lists a service the scenario does not start: %q", env["NO_PROXY"])
	}
	for k, want := range map[string]string{
		"HTTP_PROXY": "http://edge:3128", "HTTPS_PROXY": "http://edge:3128", "http_proxy": "http://edge:3128",
		"https_proxy": "http://edge:3128", "no_proxy": env["NO_PROXY"],
		"SSL_CERT_FILE": CACertPath, "NODE_EXTRA_CA_CERTS": CACertPath, "REQUESTS_CA_BUNDLE": CACertPath,
	} {
		if env[k] != want {
			t.Errorf("app %s = %q, want %q", k, env[k], want)
		}
	}
	if !slices.Contains(strs(app["volumes"]), CAVolume+":"+CAMount+":ro") {
		t.Errorf("app volumes %v miss the CA read-only", strs(app["volumes"]))
	}
	// Services get the same variables unless they set one themselves.
	for _, name := range []string{"db", "cache"} {
		senv := envOf(t, svcOf(t, doc, name))
		if senv["HTTPS_PROXY"] != ProxyURL || senv["NO_PROXY"] != env["NO_PROXY"] {
			t.Errorf("%s env = %v", name, senv)
		}
	}
	if envOf(t, svcOf(t, doc, "db"))["POSTGRES_PASSWORD"] != "test" {
		t.Error("proxy env replaced a service's own env")
	}
	// The cache owner takes no proxy: it has no network.
	if _, ok := envOf(t, svcOf(t, doc, ServiceCacheInit))["HTTP_PROXY"]; ok {
		t.Error("cache-init got proxy variables")
	}
}

func TestComposeProxyEdgeOffline(t *testing.T) {
	_, doc := buildFixture(t, proxySpec(false))
	e := svcOf(t, doc, ServiceEdge)
	cmd := strings.Join(strs(e["command"]), " ")
	for _, want := range []string{"--proxy 10.231.1.0/24", "--network offline", "--ca-dir " + CAMount, "--capture-body-bytes 4096", "--stubs " + ControlMount + "/" + StubsFile} {
		if !strings.Contains(cmd, want) {
			t.Errorf("edge command %q misses %q", cmd, want)
		}
	}
	if strings.Contains(cmd, "--upstream-roots") {
		t.Errorf("an offline edge gets upstream roots: %q", cmd)
	}
	if !slices.Contains(strs(e["volumes"]), CAVolume+":"+CAMount) {
		t.Errorf("edge volumes %v miss the writable CA volume", strs(e["volumes"]))
	}
	vols, _ := doc["volumes"].(map[string]any)
	ca, _ := vols[CAVolume].(map[string]any)
	opts, _ := ca["driver_opts"].(map[string]any)
	if opts["type"] != "tmpfs" || !strings.Contains(opts["o"].(string), "uid=1004,gid=1004") {
		t.Fatalf("CA volume = %v; it must be an in-memory volume the edge's user owns", ca)
	}
	nets, _ := doc["networks"].(map[string]any)
	outer, _ := nets[NetworkOuter].(map[string]any)
	if dopts, _ := outer["driver_opts"].(map[string]any); dopts["com.docker.network.bridge.enable_ip_masquerade"] != "false" {
		t.Fatalf("offline outer network masquerades: %v", outer)
	}
}

// TestOnlineThroughProxyOnly checks the project of an online run: only the
// edge is on the network that reaches out, the inner network stays internal,
// and the app is told to use the proxy.
func TestOnlineThroughProxyOnly(t *testing.T) {
	_, doc := buildFixture(t, proxySpec(true))
	nets, _ := doc["networks"].(map[string]any)
	inner, _ := nets[NetworkInner].(map[string]any)
	outer, _ := nets[NetworkOuter].(map[string]any)
	if inner["internal"] != true || inner["enable_ipv6"] != false {
		t.Fatalf("online inner network = %v; it must stay internal", inner)
	}
	if outer["internal"] != false || outer["driver_opts"] != nil {
		t.Fatalf("online outer network = %v; it must masquerade for the edge", outer)
	}
	svcs, _ := doc["services"].(map[string]any)
	for name := range svcs {
		s := svcOf(t, doc, name)
		if s["network_mode"] != nil && s["network_mode"] != "none" {
			t.Errorf("%s network_mode %v", name, s["network_mode"])
		}
		onOuter := slices.Contains(strs(s["networks"]), NetworkOuter)
		if m, ok := s["networks"].(map[string]any); ok {
			_, onOuter = m[NetworkOuter]
		}
		if onOuter != (name == ServiceEdge) {
			t.Errorf("%s on the outer network: %v", name, onOuter)
		}
	}
	e := svcOf(t, doc, ServiceEdge)
	cmd := strings.Join(strs(e["command"]), " ")
	if !strings.Contains(cmd, "--network online") || !strings.Contains(cmd, "--upstream-roots "+edgeRootsPath) {
		t.Fatalf("online edge command %q", cmd)
	}
	if !slices.Contains(strs(e["volumes"]), "/etc/ssl/certs/ca-certificates.crt:"+edgeRootsPath+":ro") {
		t.Fatalf("online edge volumes %v miss the host roots", strs(e["volumes"]))
	}
	if envOf(t, svcOf(t, doc, ServiceApp))["HTTPS_PROXY"] != ProxyURL {
		t.Fatal("online app is not told to use the proxy")
	}

	spec := proxySpec(true)
	spec.Proxy.UpstreamRoots = ""
	f, _ := Parse([]byte(composeFixture))
	if _, err := Compose(f, spec); err == nil {
		t.Fatal("an online run without the host's CA bundle was built")
	}
}

func TestComposeWithoutProxy(t *testing.T) {
	_, doc := buildFixture(t, testSpec())
	if _, ok := envOf(t, svcOf(t, doc, ServiceApp))["HTTP_PROXY"]; ok {
		t.Fatal("a run without a proxy spec got proxy variables")
	}
	if strings.Contains(strings.Join(strs(svcOf(t, doc, ServiceEdge)["command"]), " "), "--proxy") {
		t.Fatal("edge runs a proxy without a proxy spec")
	}
}
