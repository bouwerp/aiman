package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness/edge"
)

// proxyClientSource is a tiny HTTP client the Docker tests run in the app
// container. It uses Go's defaults, so it follows HTTP_PROXY, HTTPS_PROXY,
// NO_PROXY and SSL_CERT_FILE as the app's own runtime would.
const proxyClientSource = `package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	req, err := http.NewRequest(os.Args[1], os.Args[2], strings.NewReader("amount=100"))
	if err != nil {
		fmt.Println("ERR", err)
		os.Exit(1)
	}
	req.Header.Set("Authorization", "Bearer sk_test_docker_secret_value")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		fmt.Println("ERR", err)
		os.Exit(1)
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	fmt.Printf("STATUS %d\n%s\n", resp.StatusCode, b)
}
`

// buildProxyClient builds the client statically into dir/pc.
func buildProxyClient(t *testing.T, dir string) {
	t.Helper()
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(proxyClientSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module pc\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(dir, "pc"), ".")
	cmd.Dir = srcDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build proxy client: %v: %s", err, out)
	}
}

// proxyRun starts the app file with the capture proxy and the test client.
func proxyRun(t *testing.T, p *ProxySpec, stubs []edge.Stub) *dockerRun {
	t.Helper()
	return upRun(t, dockerAppFile, "plain", func(s *RunSpec) {
		s.Proxy = p
		buildProxyClient(t, s.Src)
		if len(stubs) > 0 {
			b, err := json.Marshal(stubs)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(s.ControlDir, StubsFile), b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func (r *dockerRun) capture() []edge.Flow {
	r.t.Helper()
	tok, err := os.ReadFile(filepath.Join(r.spec.ControlDir, ControlTokenName))
	if err != nil {
		r.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	flows, _, err := edge.Client{Socket: filepath.Join(r.spec.ControlDir, ControlSocketName), Token: string(tok)}.Capture(ctx, 0, edge.MaxCaptureLimit)
	if err != nil {
		r.t.Fatalf("capture: %v", err)
	}
	return flows
}

const noProxyEnv = "env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy "

// noPrivateKey fails when any file under the run's directories holds a
// private key.
func (r *dockerRun) noPrivateKey() {
	r.t.Helper()
	for _, root := range []string{filepath.Dir(r.spec.ControlDir)} {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !d.Type().IsRegular() {
				return nil //nolint:nilerr // unreadable entries are skipped
			}
			if b, err := os.ReadFile(path); err == nil && bytes.Contains(b, []byte("PRIVATE KEY")) {
				r.t.Errorf("%s holds a private key", path)
			}
			return nil
		})
	}
}

func findFlow(flows []edge.Flow, host string) *edge.Flow {
	for i := range flows {
		if flows[i].Host == host && flows[i].Method != "CONNECT" {
			return &flows[i]
		}
	}
	return nil
}

// TestDockerOfflineStubsAndNoEgress runs an offline run: a stubbed call is
// answered by the edge, any other external call gets the offline 502 and is
// captured, and nothing, the edge included, reaches the internet.
func TestDockerOfflineStubsAndNoEgress(t *testing.T) {
	r := proxyRun(t, &ProxySpec{Stubs: true}, []edge.Stub{
		{Host: "api.stripe.com", Path: "/v1/refunds", Status: 200, Body: []byte(`{"id":"re_1","status":"succeeded"}`)},
	})
	out, err := r.exec(ServiceApp, "/src/pc POST https://api.stripe.com/v1/refunds")
	if err != nil || !strings.Contains(out, "STATUS 200") || !strings.Contains(out, `"re_1"`) {
		t.Fatalf("stubbed call: %v: %s", err, out)
	}
	out, err = r.exec(ServiceApp, "/src/pc GET https://example.com/")
	if err != nil || !strings.Contains(out, "STATUS 502") || !strings.Contains(out, "aiman: offline sandbox, no stub for example.com/") {
		t.Fatalf("unstubbed call: %v: %s", err, out)
	}
	if out, err := r.exec(ServiceApp, noProxyEnv+"/src/pc GET https://example.com/"); err == nil {
		t.Fatalf("app reached example.com without the proxy: %s", out)
	}
	if out, err := r.exec(ServiceApp, noProxyEnv+"wget -q -T 3 -O- http://1.1.1.1/"); err == nil {
		t.Fatalf("app reached 1.1.1.1 without the proxy: %s", out)
	}
	if out, err := r.exec(ServiceEdge, "wget -q -T 3 -O- http://1.1.1.1/"); err == nil {
		t.Fatalf("an offline edge reached the internet: %s", out)
	}
	flows := r.capture()
	stub, off := findFlow(flows, "api.stripe.com"), findFlow(flows, "example.com")
	if stub == nil || stub.Outcome != edge.OutcomeStub || !stub.TLS || stub.RequestBody != "amount=100" ||
		stub.RequestHeaders.Get("Authorization") != "[redacted]" {
		t.Fatalf("stub flow = %+v", stub)
	}
	if off == nil || off.Outcome != edge.OutcomeOffline || off.Status != 502 {
		t.Fatalf("offline flow = %+v", off)
	}
	if out, err := r.exec(ServiceApp, "head -1 "+CACertPath); err != nil || !strings.Contains(out, "BEGIN CERTIFICATE") {
		t.Fatalf("app reads no CA certificate: %v: %s", err, out)
	}
	if out, err := r.exec(ServiceApp, "touch "+CAMount+"/x"); err == nil {
		t.Fatalf("app can write the CA volume: %s", out)
	}
	r.noPrivateKey()
}

// TestDockerOnlineThroughProxyOnly runs an online run: the app reaches the
// internet through the edge proxy, verified with the run CA, and not at all
// without it; the proxy refuses the host's own networks.
func TestDockerOnlineThroughProxyOnly(t *testing.T) {
	needDocker(t)
	if c, err := net.DialTimeout("tcp", "example.com:443", 5*time.Second); err != nil {
		t.Skipf("no internet from this host: %v", err)
	} else {
		_ = c.Close()
	}
	roots, err := FindUpstreamRoots()
	if err != nil {
		t.Skip(err)
	}
	r := proxyRun(t, &ProxySpec{Online: true, UpstreamRoots: roots}, nil)
	out, err := r.exec(ServiceApp, "/src/pc GET https://example.com/")
	if err != nil || !strings.Contains(out, "STATUS 200") {
		t.Fatalf("online call through the proxy: %v: %s", err, out)
	}
	if out, err := r.exec(ServiceApp, noProxyEnv+"/src/pc GET https://example.com/"); err == nil {
		t.Fatalf("app reached example.com without the proxy: %s", out)
	}
	if out, err := r.exec(ServiceApp, noProxyEnv+"wget -q -T 3 -O- http://1.1.1.1/"); err == nil {
		t.Fatalf("app reached 1.1.1.1 without the proxy: %s", out)
	}
	routes, err := r.exec(ServiceApp, "ip route")
	if err != nil || strings.Contains(routes, "default") {
		t.Fatalf("online app has a default route: %v:\n%s", err, routes)
	}
	// The outer network's gateway is the host: the proxy refuses it.
	gw := r.spec.OuterSubnet.Addr().Next().String()
	out, err = r.exec(ServiceApp, "/src/pc GET http://"+gw+":22/")
	if err != nil || !strings.Contains(out, "STATUS 502") {
		t.Fatalf("call to the host through the proxy: %v: %s", err, out)
	}
	flows := r.capture()
	f := findFlow(flows, "example.com")
	if f == nil || f.Outcome != edge.OutcomeUpstream || f.Status != 200 || !f.TLS || f.RequestHeaders.Get("Authorization") != "[redacted]" {
		t.Fatalf("online flow = %+v", f)
	}
	if h := findFlow(flows, gw); h == nil || h.Outcome != edge.OutcomeRefused {
		t.Fatalf("host flow = %+v", h)
	}
	r.noPrivateKey()
}
