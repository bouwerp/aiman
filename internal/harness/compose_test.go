package harness

import (
	"bytes"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// composeFixture is a harness file with an app, a root postgres service, a
// non-root service and a scenario using both.
const composeFixture = `version: 1
app:
  build: go build -o /out/app ./cmd/app
  run: /out/app serve --addr :8080
  ports: [{name: web, container: 8080}, {name: admin, container: 9090}]
services:
  db:
    image: postgres:16
    env: {POSTGRES_PASSWORD: test}
    ready: pg_isready -h db -U postgres
    database: {kind: postgres, name: app}
    root: true
    caps: [CHOWN, SETUID, SETGID, DAC_OVERRIDE, FOWNER]
    limits: {memory: 1g}
  cache:
    image: redis:7
    ready: redis-cli ping
  unused:
    image: postgres:16
    database: {kind: postgres}
scenarios:
  - id: refund
    services: [db, cache]
    steps:
      - do: "Open {{web}}"
`

func testSpec() RunSpec {
	return RunSpec{
		RunID:        "r1a2b3",
		Scenario:     "refund",
		Src:          "/home/u/.aiman/harness/s1/r1a2b3/src",
		Out:          "/home/u/.aiman/harness/s1/r1a2b3/out",
		ControlDir:   "/home/u/.aiman/harness/s1/r1a2b3/control",
		AimanBin:     "/home/u/.local/bin/aiman",
		UID:          1004,
		GID:          1004,
		AppImage:     "golang:1.25",
		EdgeImage:    "alpine:3.20",
		InnerSubnet:  netip.MustParsePrefix("10.231.1.0/24"),
		OuterSubnet:  netip.MustParsePrefix("10.231.2.0/24"),
		ReadPassword: "ro-secret",
	}
}

func buildFixture(t *testing.T, spec RunSpec) (*Project, map[string]any) {
	t.Helper()
	f, err := Parse([]byte(composeFixture))
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

func svcOf(t *testing.T, doc map[string]any, name string) map[string]any {
	t.Helper()
	svcs, _ := doc["services"].(map[string]any)
	s, ok := svcs[name].(map[string]any)
	if !ok {
		t.Fatalf("no service %q in %v", name, keys(svcs))
	}
	return s
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func strs(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

func TestComposeServicesAndProject(t *testing.T) {
	p, doc := buildFixture(t, testSpec())
	if p.Name != "harness-r1a2b3" || doc["name"] != "harness-r1a2b3" {
		t.Fatalf("project name %q / %v", p.Name, doc["name"])
	}
	svcs, _ := doc["services"].(map[string]any)
	want := []string{"app", "cache", "cache-init", "db", "edge"}
	if got := keys(svcs); !slices.Equal(got, want) {
		t.Fatalf("services %v, want %v (unused service must be left out)", got, want)
	}
}

func TestComposeDeterministic(t *testing.T) {
	a, _ := buildFixture(t, testSpec())
	for range 5 {
		b, _ := buildFixture(t, testSpec())
		if !bytes.Equal(a.YAML, b.YAML) {
			t.Fatalf("compose output differs between builds")
		}
	}
}

func TestComposePullNever(t *testing.T) {
	_, doc := buildFixture(t, testSpec())
	svcs, _ := doc["services"].(map[string]any)
	for name := range svcs {
		if got := svcOf(t, doc, name)["pull_policy"]; got != "never" {
			t.Errorf("%s pull_policy %v, want never", name, got)
		}
	}
}

func TestComposeServiceImages(t *testing.T) {
	spec := testSpec()
	spec.ServiceImages = map[string]string{"db": "sha256:abc"}
	_, doc := buildFixture(t, spec)
	if got := svcOf(t, doc, "db")["image"]; got != "sha256:abc" {
		t.Errorf("db image %v, want the override", got)
	}
	if got := svcOf(t, doc, "cache")["image"]; got != "redis:7" {
		t.Errorf("cache image %v, want the file's", got)
	}
	if got := svcOf(t, doc, "app")["image"]; got != "golang:1.25" {
		t.Errorf("app image %v", got)
	}
	if got := svcOf(t, doc, "cache-init")["image"]; got != "golang:1.25" {
		t.Errorf("cache-init image %v", got)
	}
	if got := svcOf(t, doc, "edge")["image"]; got != "alpine:3.20" {
		t.Errorf("edge image %v", got)
	}
}

func TestComposeAppProfile(t *testing.T) {
	_, doc := buildFixture(t, testSpec())
	app := svcOf(t, doc, "app")
	if app["read_only"] != true {
		t.Error("app is not read_only")
	}
	if app["user"] != "1004:1004" {
		t.Errorf("app user %v", app["user"])
	}
	if got := strs(app["cap_drop"]); !slices.Equal(got, []string{"ALL"}) {
		t.Errorf("app cap_drop %v", got)
	}
	if _, ok := app["cap_add"]; ok {
		t.Error("app has cap_add")
	}
	if got := strs(app["security_opt"]); !slices.Contains(got, "no-new-privileges:true") {
		t.Errorf("app security_opt %v", got)
	}
	if got := strs(app["tmpfs"]); !slices.Equal(got, []string{"/tmp:exec"}) {
		t.Errorf("app tmpfs %v", got)
	}
	vols := strs(app["volumes"])
	for _, want := range []string{testSpec().Src + ":/src", testSpec().Out + ":/out", "gomod:/cache/gomod"} {
		if !slices.Contains(vols, want) {
			t.Errorf("app volumes %v lack %s", vols, want)
		}
	}
	for _, v := range vols {
		if strings.Contains(v, "control") || strings.Contains(v, "docker.sock") {
			t.Errorf("app mounts %s", v)
		}
	}
	if got := strs(app["command"]); !slices.Equal(got, []string{"sh", "-c", "/out/app serve --addr :8080"}) {
		t.Errorf("app command %v", got)
	}
	if app["working_dir"] != "/src" {
		t.Errorf("app working_dir %v", app["working_dir"])
	}
	init := svcOf(t, doc, "cache-init")
	if init["network_mode"] != "none" || init["user"] != "0:0" {
		t.Errorf("cache-init network_mode %v user %v", init["network_mode"], init["user"])
	}
	if got := strs(init["cap_add"]); !slices.Equal(got, []string{"CHOWN"}) {
		t.Errorf("cache-init cap_add %v", got)
	}
}

func TestInnerNetwork(t *testing.T) {
	_, doc := buildFixture(t, testSpec())
	nets, _ := doc["networks"].(map[string]any)
	inner, _ := nets["inner"].(map[string]any)
	if inner["internal"] != true {
		t.Errorf("inner network internal %v", inner["internal"])
	}
	if inner["enable_ipv6"] != false {
		t.Errorf("inner network enable_ipv6 %v", inner["enable_ipv6"])
	}
	if !strings.Contains(string(mustYAML(t, inner)), "10.231.1.0/24") {
		t.Errorf("inner network lacks its subnet: %v", inner)
	}
	outer, _ := nets["outer"].(map[string]any)
	if outer["internal"] == true || outer["enable_ipv6"] != false {
		t.Errorf("outer network %v", outer)
	}
	if !strings.Contains(string(mustYAML(t, outer)), "10.231.2.0/24") {
		t.Errorf("outer network lacks its subnet: %v", outer)
	}
	for _, name := range []string{"app", "db", "cache"} {
		got := networksOf(t, svcOf(t, doc, name))
		if !slices.Equal(got, []string{"inner"}) {
			t.Errorf("%s networks %v, want only inner", name, got)
		}
		if _, ok := svcOf(t, doc, name)["network_mode"]; ok {
			t.Errorf("%s sets network_mode", name)
		}
	}
	if got := networksOf(t, svcOf(t, doc, "edge")); !slices.Equal(got, []string{"inner", "outer"}) {
		t.Errorf("edge networks %v", got)
	}
}

func networksOf(t *testing.T, svc map[string]any) []string {
	t.Helper()
	switch n := svc["networks"].(type) {
	case []any:
		out := strs(n)
		slices.Sort(out)
		return out
	case map[string]any:
		return keys(n)
	default:
		return nil
	}
}

func mustYAML(t *testing.T, v any) []byte {
	t.Helper()
	b, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEdgeOnlyPublisher(t *testing.T) {
	p, doc := buildFixture(t, testSpec())
	svcs, _ := doc["services"].(map[string]any)
	for name := range svcs {
		if name == "edge" {
			continue
		}
		s := svcOf(t, doc, name)
		if _, ok := s["ports"]; ok {
			t.Errorf("%s publishes ports", name)
		}
		if _, ok := s["expose"]; ok {
			t.Errorf("%s exposes ports", name)
		}
		if s["privileged"] == true {
			t.Errorf("%s is privileged", name)
		}
	}
	edge := svcOf(t, doc, "edge")
	ports := strs(edge["ports"])
	if len(ports) != len(p.Relays) || len(ports) == 0 {
		t.Fatalf("edge ports %v, relays %v", ports, p.Relays)
	}
	for _, r := range p.Relays {
		want := fmt.Sprintf("127.0.0.1::%d", r.Port)
		if !slices.Contains(ports, want) {
			t.Errorf("edge ports %v lack %s", ports, want)
		}
	}
	for _, pt := range ports {
		if !strings.HasPrefix(pt, "127.0.0.1:") {
			t.Errorf("edge port %s is not on host 127.0.0.1", pt)
		}
	}
	sysctls, _ := edge["sysctls"].(map[string]any)
	for k, want := range map[string]string{
		"net.ipv4.ip_forward":                "0",
		"net.ipv6.conf.all.forwarding":       "0",
		"net.ipv6.conf.all.disable_ipv6":     "1",
		"net.ipv6.conf.default.disable_ipv6": "1",
	} {
		if fmt.Sprint(sysctls[k]) != want {
			t.Errorf("edge sysctl %s = %v, want %s", k, sysctls[k], want)
		}
	}
	if edge["read_only"] != true || edge["user"] != "1004:1004" {
		t.Errorf("edge read_only %v user %v", edge["read_only"], edge["user"])
	}
	if got := strs(edge["cap_drop"]); !slices.Equal(got, []string{"ALL"}) {
		t.Errorf("edge cap_drop %v", got)
	}
	if !slices.Contains(strs(edge["security_opt"]), "no-new-privileges:true") {
		t.Error("edge lacks no-new-privileges")
	}
	vols := strs(edge["volumes"])
	spec := testSpec()
	for _, want := range []string{spec.AimanBin + ":/aiman:ro", spec.ControlDir + ":/control"} {
		if !slices.Contains(vols, want) {
			t.Errorf("edge volumes %v lack %s", vols, want)
		}
	}
	cmd := strings.Join(strs(edge["command"]), " ")
	for _, want := range []string{
		"/aiman harness edge",
		"--control /control/edge.sock",
		"--token-file /control/token",
		"--outer 10.231.2.0/24",
		"--inner 10.231.1.0/24",
		"--relay web=10001=app:8080",
		"--relay admin=10002=app:9090",
		"--relay db=10003=db:5432",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("edge command %q lacks %q", cmd, want)
		}
	}
}

func TestComposeRelayMap(t *testing.T) {
	p, _ := buildFixture(t, testSpec())
	want := []Relay{
		{Name: "web", Service: "app", Port: 10001, Target: "app:8080"},
		{Name: "admin", Service: "app", Port: 10002, Target: "app:9090"},
		{Name: "db", Service: "db", Port: 10003, Target: "db:5432"},
	}
	if !slices.Equal(p.Relays, want) {
		t.Fatalf("relays %+v, want %+v", p.Relays, want)
	}
}

func TestServiceProfile(t *testing.T) {
	_, doc := buildFixture(t, testSpec())
	db := svcOf(t, doc, "db")
	if db["user"] != "0:0" {
		t.Errorf("root service user %v, want 0:0", db["user"])
	}
	if db["read_only"] != true {
		t.Error("db is not read_only")
	}
	if got := strs(db["cap_drop"]); !slices.Equal(got, []string{"ALL"}) {
		t.Errorf("db cap_drop %v", got)
	}
	if got := strs(db["cap_add"]); !slices.Equal(got, []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID"}) {
		t.Errorf("db cap_add %v", got)
	}
	if got := strs(db["security_opt"]); slices.Contains(got, "no-new-privileges:true") {
		t.Errorf("service has no-new-privileges, which blocks the setuid drop: %v", got)
	}
	if got := strs(db["tmpfs"]); !slices.Equal(got, []string{"/run", "/tmp"}) {
		t.Errorf("db tmpfs %v", got)
	}
	vols := strs(db["volumes"])
	initMount := "./" + InitDBFile + ":/docker-entrypoint-initdb.d/00-aiman-ro.sql:ro"
	if !slices.Equal(vols, []string{"data-db:/var/lib/postgresql/data", initMount}) {
		t.Errorf("db volumes %v, want the named data volume and the init script", vols)
	}
	env, _ := db["environment"].(map[string]any)
	if env["POSTGRES_PASSWORD"] != "test" || env[PostgresReadPasswordEnv] != "ro-secret" || env["POSTGRES_DB"] != "app" {
		t.Errorf("db environment %v", env)
	}
	p, _ := buildFixture(t, testSpec())
	if got := p.Files[InitDBFile]; !bytes.Equal(got, PostgresInitSQL()) {
		t.Errorf("project files %v lack the init script", p.Files)
	}
	hc, _ := db["healthcheck"].(map[string]any)
	if got := strs(hc["test"]); !slices.Equal(got, []string{"CMD-SHELL", "pg_isready -h db -U postgres"}) {
		t.Errorf("db healthcheck %v", hc)
	}
	vtop, _ := doc["volumes"].(map[string]any)
	if _, ok := vtop["data-db"]; !ok {
		t.Errorf("top-level volumes %v lack data-db", keys(vtop))
	}

	cache := svcOf(t, doc, "cache")
	if _, ok := cache["user"]; ok {
		t.Errorf("non-root service sets user %v; it must run as the image's user", cache["user"])
	}
	if _, ok := cache["cap_add"]; ok {
		t.Errorf("cache cap_add %v", cache["cap_add"])
	}
	if strings.Contains(string(mustYAML(t, cache)), "initdb") {
		t.Error("non-postgres service gets the postgres init script")
	}
	if strings.Contains(string(mustYAML(t, cache)), "docker.sock") {
		t.Error("cache mounts the docker socket")
	}
}

func TestPostgresInitSQL(t *testing.T) {
	sql := string(PostgresInitSQL())
	for _, want := range []string{"aiman_ro", "pg_read_all_data", `\getenv`, PostgresReadPasswordEnv, "NOSUPERUSER"} {
		if !strings.Contains(sql, want) {
			t.Errorf("init SQL lacks %q:\n%s", want, sql)
		}
	}
}

func TestDefaultLimits(t *testing.T) {
	spec := testSpec()
	spec.ServiceMemory = 3 << 30
	_, doc := buildFixture(t, spec)
	want := map[string][3]any{
		"app":        {DefaultAppMemory, DefaultCPUs, DefaultPIDs},
		"cache-init": {DefaultAppMemory, DefaultCPUs, DefaultPIDs},
		"db":         {int64(1 << 30), DefaultCPUs, DefaultPIDs},
		"cache":      {int64(3 << 30), DefaultCPUs, DefaultPIDs},
		"edge":       {EdgeLimits.Memory, EdgeLimits.CPUs, EdgeLimits.PIDs},
	}
	for name, w := range want {
		s := svcOf(t, doc, name)
		if fmt.Sprint(s["mem_limit"]) != fmt.Sprint(w[0]) ||
			fmt.Sprint(s["cpus"]) != fmt.Sprint(w[1]) ||
			fmt.Sprint(s["pids_limit"]) != fmt.Sprint(w[2]) {
			t.Errorf("%s limits mem %v cpus %v pids %v, want %v", name, s["mem_limit"], s["cpus"], s["pids_limit"], w)
		}
	}
}

func TestComposeRejectsBadSpec(t *testing.T) {
	f, err := Parse([]byte(composeFixture))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*RunSpec){
		"bad run id":        func(s *RunSpec) { s.RunID = "Bad/ID" },
		"unknown scenario":  func(s *RunSpec) { s.Scenario = "nope" },
		"relative src":      func(s *RunSpec) { s.Src = "src" },
		"relative control":  func(s *RunSpec) { s.ControlDir = "control" },
		"relative aiman":    func(s *RunSpec) { s.AimanBin = "aiman" },
		"no app image":      func(s *RunSpec) { s.AppImage = "" },
		"no edge image":     func(s *RunSpec) { s.EdgeImage = "" },
		"no inner subnet":   func(s *RunSpec) { s.InnerSubnet = netip.Prefix{} },
		"ipv6 subnet":       func(s *RunSpec) { s.OuterSubnet = netip.MustParsePrefix("fd00::/64") },
		"overlapping":       func(s *RunSpec) { s.OuterSubnet = netip.MustParsePrefix("10.231.1.128/25") },
		"no read password":  func(s *RunSpec) { s.ReadPassword = "" },
		"root uid":          func(s *RunSpec) { s.UID = 0 },
		"colon in src":      func(s *RunSpec) { s.Src = "/a:b/src" },
		"unknown svc image": func(s *RunSpec) { s.ServiceImages = map[string]string{"ghost": "x"} },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			spec := testSpec()
			mut(&spec)
			if _, err := Compose(f, spec); err == nil {
				t.Fatal("Compose accepted a bad spec")
			}
		})
	}
}

// Parse refuses a port named like a service; Compose still refuses a File
// built some other way.
func TestComposeRelayNameClash(t *testing.T) {
	f, err := Parse([]byte(composeFixture))
	if err != nil {
		t.Fatal(err)
	}
	for i := range f.App.Ports {
		if f.App.Ports[i].Name == "admin" {
			f.App.Ports[i].Name = "db"
		}
	}
	if _, err := Compose(f, testSpec()); err == nil || !strings.Contains(err.Error(), "db") {
		t.Fatalf("want a relay name clash error, got %v", err)
	}
}
