package harness

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests start real compose projects. They run only with
// AIMAN_TEST_DOCKER=1, build a static aiman binary for the edge, and may pull
// busybox and postgres:16; the builder itself never pulls.

const (
	dockerTestImage    = "busybox:1.36"
	dockerTestPostgres = "postgres:16"
)

// dockerApp serves "ok" on 8080 from the read-only app container.
const dockerAppFile = `version: 1
app:
  run: echo ok > /tmp/index.html && exec httpd -f -p 8080 -h /tmp
  ports: [{name: web, container: 8080}]
scenarios:
  - id: plain
`

const dockerPostgresFile = `version: 1
app:
  run: exec sleep 600
services:
  db:
    image: postgres:16
    env: {POSTGRES_PASSWORD: test}
    ready: pg_isready -h 127.0.0.1 -U postgres
    database: {kind: postgres, name: app}
    root: true
    caps: [CHOWN, SETUID, SETGID, DAC_OVERRIDE, FOWNER]
    limits: {memory: 1g}
scenarios:
  - id: pg
    services: [db]
`

func needDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("AIMAN_TEST_DOCKER") != "1" {
		t.Skip("set AIMAN_TEST_DOCKER=1 to run Docker tests")
	}
}

var (
	edgeBinOnce sync.Once
	edgeBinPath string
	edgeBinErr  error
	pullOnce    sync.Map
)

// TestMain removes the edge binary the Docker tests built.
func TestMain(m *testing.M) {
	code := m.Run()
	if edgeBinPath != "" {
		_ = os.RemoveAll(filepath.Dir(edgeBinPath))
	}
	os.Exit(code)
}

// edgeBinary builds cmd/aiman without cgo, so it runs in a busybox edge.
func edgeBinary(t *testing.T) string {
	t.Helper()
	edgeBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "aiman-edge-bin-")
		if err != nil {
			edgeBinErr = err
			return
		}
		root, err := exec.Command("go", "env", "GOMOD").Output()
		if err != nil {
			edgeBinErr = err
			return
		}
		edgeBinPath = filepath.Join(dir, "aiman")
		cmd := exec.Command("go", "build", "-o", edgeBinPath, "./cmd/aiman")
		cmd.Dir = filepath.Dir(strings.TrimSpace(string(root)))
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			edgeBinErr = fmt.Errorf("build aiman: %w: %s", err, out)
		}
	})
	if edgeBinErr != nil {
		t.Fatal(edgeBinErr)
	}
	return edgeBinPath
}

// ensureImage pulls ref once when it is not on the host. Only tests pull.
func ensureImage(t *testing.T, ref string) {
	t.Helper()
	once, _ := pullOnce.LoadOrStore(ref, &sync.Once{})
	once.(*sync.Once).Do(func() {
		if exec.Command("docker", "image", "inspect", ref).Run() == nil {
			return
		}
		if out, err := exec.Command("docker", "pull", ref).CombinedOutput(); err != nil {
			t.Fatalf("pull %s: %v: %s", ref, err, out)
		}
	})
}

func randomSubnets(t *testing.T) (inner, outer netip.Prefix) {
	t.Helper()
	n, err := rand.Int(rand.Reader, big.NewInt(120))
	if err != nil {
		t.Fatal(err)
	}
	m, err := rand.Int(rand.Reader, big.NewInt(254))
	if err != nil {
		t.Fatal(err)
	}
	b := byte(130 + n.Int64())
	c := byte(m.Int64())
	return netip.PrefixFrom(netip.AddrFrom4([4]byte{10, b, c, 0}), 25),
		netip.PrefixFrom(netip.AddrFrom4([4]byte{10, b, c, 128}), 25)
}

type dockerRun struct {
	t       *testing.T
	project *Project
	file    string
	spec    RunSpec
}

func (r *dockerRun) compose(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"compose", "-f", r.file, "-p", r.project.Name}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func (r *dockerRun) exec(service string, script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return r.compose(ctx, "exec", "-T", service, "sh", "-c", script)
}

// upRun builds the project for the file and scenario, writes the control
// token and starts every service; each mut may change the spec first, with
// its directories made. Cleanup always runs compose down -v.
func upRun(t *testing.T, fileYAML, scenario string, mut ...func(*RunSpec)) *dockerRun {
	t.Helper()
	needDocker(t)
	ensureImage(t, dockerTestImage)
	f, err := Parse([]byte(fileYAML))
	if err != nil {
		t.Fatal(err)
	}
	for name := range f.Services {
		ensureImage(t, f.ServiceImageRef(name))
	}
	dir := t.TempDir()
	for _, d := range []string{"src", "out", "control"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "control", ControlTokenName), []byte(hex.EncodeToString(token)), 0o600); err != nil {
		t.Fatal(err)
	}
	inner, outer := randomSubnets(t)
	id := make([]byte, 5)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	spec := RunSpec{
		RunID: "t" + hex.EncodeToString(id), Scenario: scenario,
		Src: filepath.Join(dir, "src"), Out: filepath.Join(dir, "out"), ControlDir: filepath.Join(dir, "control"),
		AimanBin: edgeBinary(t), UID: os.Getuid(), GID: os.Getgid(),
		AppImage: dockerTestImage, EdgeImage: dockerTestImage,
		InnerSubnet: inner, OuterSubnet: outer, ReadPassword: "ro-" + hex.EncodeToString(id),
	}
	for _, m := range mut {
		m(&spec)
	}
	p, err := Compose(f, spec)
	if err != nil {
		t.Fatal(err)
	}
	r := &dockerRun{t: t, project: p, file: filepath.Join(dir, "compose.yaml"), spec: spec}
	if err := os.WriteFile(r.file, p.YAML, 0o600); err != nil {
		t.Fatal(err)
	}
	for rel, b := range p.Files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if out, err := r.compose(ctx, "down", "-v", "--remove-orphans", "-t", "2"); err != nil {
			t.Logf("compose down: %v: %s", err, out)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if out, err := r.compose(ctx, "up", "-d", "--wait", "--wait-timeout", "120"); err != nil {
		logs, _ := r.compose(context.Background(), "logs", "--no-color")
		t.Fatalf("compose up: %v: %s\n%s\n%s", err, out, logs, p.YAML)
	}
	r.waitControl()
	return r
}

// waitControl waits for the edge's control socket, which it creates after
// its relays listen.
func (r *dockerRun) waitControl() {
	sock := filepath.Join(r.spec.ControlDir, ControlSocketName)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	logs, _ := r.compose(context.Background(), "logs", "--no-color", ServiceEdge)
	r.t.Fatalf("edge made no control socket: %s", logs)
}

func (r *dockerRun) hostPort(containerPort int) string {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := r.compose(ctx, "port", ServiceEdge, fmt.Sprint(containerPort))
	if err != nil {
		r.t.Fatalf("compose port: %v: %s", err, out)
	}
	return strings.TrimSpace(out)
}

func TestOfflineAppHasNoEgress(t *testing.T) {
	r := upRun(t, dockerAppFile, "plain")
	if out, err := r.exec(ServiceApp, "wget -q -T 3 -O- http://1.1.1.1/"); err == nil {
		t.Fatalf("app reached the internet: %s", out)
	}
	if out, err := r.exec(ServiceApp, "wget -q -T 3 -O- http://example.com/"); err == nil {
		t.Fatalf("app resolved and reached example.com: %s", out)
	}
	routes, err := r.exec(ServiceApp, "ip route")
	if err != nil {
		t.Fatalf("ip route: %v: %s", err, routes)
	}
	if strings.Contains(routes, "default") {
		t.Fatalf("app has a default route:\n%s", routes)
	}
}

func TestAppCannotReachEdgeControl(t *testing.T) {
	r := upRun(t, dockerAppFile, "plain")
	// The relay works from the host through 127.0.0.1.
	addr := r.hostPort(r.project.Relays[0].Port)
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("relay published on %s, want 127.0.0.1", addr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("relay from host: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("relay answered %q", body)
	}
	// The app sees no control directory and cannot reach a relay, neither by
	// the edge's inner name nor by its outer address.
	if out, err := r.exec(ServiceApp, "test -e "+ControlMount); err == nil {
		t.Fatalf("app sees %s: %s", ControlMount, out)
	}
	port := fmt.Sprint(r.project.Relays[0].Port)
	if out, err := r.exec(ServiceApp, "wget -q -T 3 -O- http://edge:"+port+"/"); err == nil {
		t.Fatalf("app reached the relay on the inner network: %s", out)
	}
	outerEdge := r.spec.OuterSubnet.Addr().Next().Next().String()
	if out, err := r.exec(ServiceApp, "wget -q -T 3 -O- http://"+outerEdge+":"+port+"/"); err == nil {
		t.Fatalf("app reached the relay on the outer address: %s", out)
	}
}

func TestInnerNetworkNoIPv6(t *testing.T) {
	r := upRun(t, dockerAppFile, "plain")
	for _, svc := range []string{ServiceApp, ServiceEdge} {
		out, err := r.exec(svc, "cat /proc/net/if_inet6; ip -6 route")
		if err != nil {
			t.Fatalf("%s: %v: %s", svc, err, out)
		}
		if strings.TrimSpace(out) != "" {
			t.Fatalf("%s has IPv6 addresses or routes:\n%s", svc, out)
		}
	}
	out, err := exec.Command("docker", "network", "inspect", "-f", "{{.EnableIPv6}} {{.Internal}}", r.project.Name+"_"+NetworkInner).CombinedOutput()
	if err != nil {
		t.Fatalf("network inspect: %v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != "false true" {
		t.Fatalf("inner network EnableIPv6/Internal = %s", out)
	}
}

func TestEdgeNoIPForward(t *testing.T) {
	r := upRun(t, dockerAppFile, "plain")
	out, err := r.exec(ServiceEdge, "cat /proc/sys/net/ipv4/ip_forward /proc/sys/net/ipv6/conf/all/disable_ipv6")
	if err != nil {
		t.Fatalf("edge: %v: %s", err, out)
	}
	if got := strings.Fields(out); len(got) != 2 || got[0] != "0" || got[1] != "1" {
		t.Fatalf("edge ip_forward and disable_ipv6 = %q, want 0 and 1", out)
	}
}

func TestPostgresServiceProfile(t *testing.T) {
	ensureImage(t, dockerTestPostgres)
	r := upRun(t, dockerPostgresFile, "pg")
	q := func(user, sql string) (string, error) {
		return r.exec("db", fmt.Sprintf(`PGPASSWORD="$%s" psql -h 127.0.0.1 -U %s -d app -tAc %q`,
			map[bool]string{true: "AIMAN_RO_PASSWORD", false: "POSTGRES_PASSWORD"}[user == PostgresReadRole], user, sql))
	}
	out, err := q("postgres", "select rolsuper, pg_has_role('aiman_ro','pg_read_all_data','member') from pg_roles where rolname='aiman_ro'")
	if err != nil || strings.TrimSpace(out) != "f|t" {
		t.Fatalf("aiman_ro role: %v: %q", err, out)
	}
	if out, err := q("postgres", "create table orders (id int); insert into orders values (42)"); err != nil {
		t.Fatalf("superuser write: %v: %s", err, out)
	}
	out, err = q(PostgresReadRole, "select id from orders")
	if err != nil || strings.TrimSpace(out) != "42" {
		t.Fatalf("read role read: %v: %q", err, out)
	}
	if out, err := q(PostgresReadRole, "insert into orders values (1)"); err == nil {
		t.Fatalf("read role wrote: %s", out)
	}
	if out, err := q(PostgresReadRole, "create table x (id int)"); err == nil {
		t.Fatalf("read role created a table: %s", out)
	}
	if out, err := r.exec("db", "touch /etc/aiman-probe"); err == nil {
		t.Fatalf("service root filesystem is writable: %s", out)
	}
}
