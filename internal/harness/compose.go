package harness

import (
	"embed"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Compose service, network and mount names of a run's project.
const (
	ServiceApp       = "app"
	ServiceEdge      = "edge"
	ServiceCacheInit = "cache-init"
	NetworkInner     = "inner"
	NetworkOuter     = "outer"

	// ControlMount is where the edge sees the run's control directory.
	ControlMount = "/control"
	// ControlSocketName and ControlTokenName are the control socket and the
	// token file inside the control directory. Serve writes the token before
	// the edge starts; the edge creates the socket.
	ControlSocketName = "edge.sock"
	ControlTokenName  = "token"
	// EdgeBinPath is where the aiman binary is mounted read-only in the edge.
	EdgeBinPath = "/aiman"
	// FirstRelayPort is the edge's first relay port. Relays take consecutive
	// ports in Project.Relays order.
	FirstRelayPort = 10001

	// PostgresReadRole is the read role the init script creates, and
	// PostgresReadPasswordEnv the variable that carries its per-run password.
	PostgresReadRole        = "aiman_ro"
	PostgresReadPasswordEnv = "AIMAN_RO_PASSWORD" //nolint:gosec // G101: a variable name, not a credential
	postgresPort            = 5432
	postgresDataDir         = "/var/lib/postgresql/data"
	postgresInitDir         = "/docker-entrypoint-initdb.d/"
	initDBFile              = "00-aiman-ro.sql"

	// InitDBFile is the init script's path in Project.Files.
	InitDBFile = "initdb/" + initDBFile
)

// Hardening constants, copied from internal/review/sandbox.go.
const (
	noNewPrivileges = "no-new-privileges:true"
	capDropAll      = "ALL"
	pullNever       = "never"
)

//go:embed initdb/*.sql
var initDB embed.FS

// PostgresInitSQL is the init script mounted into every postgres service's
// /docker-entrypoint-initdb.d/.
func PostgresInitSQL() []byte {
	b, err := initDB.ReadFile(InitDBFile)
	if err != nil {
		panic(err) // embedded at build time
	}
	return b
}

// RunSpec is what Compose needs besides the harness file.
type RunSpec struct {
	// RunID names the compose project harness-<RunID>.
	RunID    string
	Scenario string
	// Src and Out are host directories mounted at /src and /out in the app.
	Src, Out string
	// ControlDir is the host control directory, mounted only in the edge.
	ControlDir string
	// AimanBin is the host aiman binary the edge runs, mounted read-only.
	AimanBin string
	// UID and GID run the app and the edge. Root is refused.
	UID, GID int
	// AppImage runs the app and cache-init; EdgeImage runs the edge.
	AppImage, EdgeImage string
	// ServiceImages replaces a service's image, keyed by service name (serve
	// passes image IDs). A service not listed runs the file's image.
	ServiceImages map[string]string
	// InnerSubnet and OuterSubnet are the two networks' IPv4 subnets.
	InnerSubnet, OuterSubnet netip.Prefix
	// ReadPassword is aiman_ro's password, required when the scenario starts
	// a postgres service.
	ReadPassword string
	// ServiceMemory is harness.service_memory; 0 is DefaultServiceMemory.
	ServiceMemory int64
	// Debug runs the app under a debug adapter on the debug network; nil is
	// a plain run. DebugSubnet is that network's IPv4 subnet.
	Debug       *DebugSpec
	DebugSubnet netip.Prefix
	// AdapterImage runs the adapter container when Debug has an Adapter.
	AdapterImage string
}

// Relay is one edge relay: the edge listens on Port on its outer address,
// published on host 127.0.0.1 at a port Docker picks, and dials Target on the
// inner network. Service is the container behind Target.
type Relay struct {
	Name    string
	Service string
	Port    int
	Target  string
	// Kind is empty for a plain TCP relay and RelayKindDAP for the debugger,
	// which admits one client at a time.
	Kind string
}

// Project is a run's compose project. Files are paths relative to the
// directory that holds the compose file, which the caller writes there with
// mode 0444 before compose up; a read-only service takes a file only as a
// bind mount, so the postgres init script cannot travel inside the YAML.
type Project struct {
	Name   string
	YAML   []byte
	Relays []Relay
	Files  map[string][]byte
}

var runIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,40}$`)

// Compose builds the compose project of one run of spec.Scenario. The file is
// aiman's, never the repository's; output is deterministic for equal input.
func Compose(f *File, spec RunSpec) (*Project, error) {
	sc, err := checkSpec(f, spec)
	if err != nil {
		return nil, err
	}
	relays, err := relaysFor(f, sc, spec)
	if err != nil {
		return nil, err
	}
	doc := composeFile{
		Name:     "harness-" + spec.RunID,
		Services: map[string]composeService{},
		Networks: map[string]composeNetwork{
			NetworkInner: network(spec.InnerSubnet, true),
			NetworkOuter: network(spec.OuterSubnet, false),
		},
		Volumes: map[string]map[string]any{"gomod": {}, "gocache": {}, "npm": {}},
	}
	app, edge := appService(f, spec), edgeService(spec, relays)
	if spec.Debug != nil {
		doc.Networks[NetworkDebug] = network(spec.DebugSubnet, true)
		debugApp(&app, spec)
		debugEdge(&edge, spec)
		if spec.Debug.Adapter != nil {
			doc.Services[ServiceAdapter] = adapterService(spec)
		}
	}
	doc.Services[ServiceApp] = app
	doc.Services[ServiceCacheInit] = cacheInitService(f, spec)
	doc.Services[ServiceEdge] = edge
	files := map[string][]byte{}
	for _, name := range sc.Services {
		doc.Services[name] = serviceService(f, spec, name)
		if isPostgres(f.Services[name]) {
			doc.Volumes[dataVolume(name)] = map[string]any{}
			files[InitDBFile] = PostgresInitSQL()
		}
	}
	for name, svc := range doc.Services {
		doc.Services[name] = escapeInterpolation(svc)
	}
	raw, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return &Project{Name: doc.Name, YAML: raw, Relays: relays, Files: files}, nil
}

// escapeInterpolation doubles every $ in what a container runs or reads, so
// compose passes it to the container's shell as written instead of filling
// it from serve's own environment.
func escapeInterpolation(s composeService) composeService {
	esc := func(v string) string { return strings.ReplaceAll(v, "$", "$$") }
	cmd := make([]string, len(s.Command))
	for i, c := range s.Command {
		cmd[i] = esc(c)
	}
	if s.Command != nil {
		s.Command = cmd
	}
	if s.Environment != nil {
		env := make(map[string]string, len(s.Environment))
		for k, v := range s.Environment {
			env[k] = esc(v)
		}
		s.Environment = env
	}
	if s.Healthcheck != nil {
		hc := *s.Healthcheck
		hc.Test = make([]string, len(s.Healthcheck.Test))
		for i, c := range s.Healthcheck.Test {
			hc.Test[i] = esc(c)
		}
		s.Healthcheck = &hc
	}
	return s
}

func checkSpec(f *File, spec RunSpec) (*Scenario, error) {
	if f == nil {
		return nil, errors.New("harness compose: no harness file")
	}
	if !runIDRe.MatchString(spec.RunID) {
		return nil, fmt.Errorf("harness compose: run id %q must match %s", spec.RunID, runIDRe)
	}
	sc, ok := f.Scenario(spec.Scenario)
	if !ok {
		return nil, &Error{Code: CodeUnknownScenario, Msg: fmt.Sprintf("no scenario %q in the harness file", spec.Scenario)}
	}
	if spec.UID <= 0 || spec.GID < 0 {
		return nil, errors.New("harness compose: the app and the edge never run as root; set a non-zero uid")
	}
	if spec.AppImage == "" || spec.EdgeImage == "" {
		return nil, errors.New("harness compose: app and edge images are required")
	}
	if err := checkPaths(spec); err != nil {
		return nil, err
	}
	if err := checkSubnets(spec.InnerSubnet, spec.OuterSubnet); err != nil {
		return nil, err
	}
	if err := checkDebug(f, spec); err != nil {
		return nil, err
	}
	for name := range spec.ServiceImages {
		if !slices.Contains(sc.Services, name) {
			return nil, fmt.Errorf("harness compose: image given for %q, which scenario %s does not start", name, sc.ID)
		}
	}
	for _, name := range sc.Services {
		if isPostgres(f.Services[name]) && spec.ReadPassword == "" {
			return nil, fmt.Errorf("harness compose: postgres service %s needs a read role password", name)
		}
	}
	return sc, nil
}

// checkPaths refuses relative paths and paths with a colon or comma, which a
// compose short volume syntax would split.
func checkPaths(spec RunSpec) error {
	for label, p := range map[string]string{"src": spec.Src, "out": spec.Out, "control": spec.ControlDir, "aiman binary": spec.AimanBin} {
		if !filepath.IsAbs(p) || strings.ContainsAny(p, ":,\n") {
			return fmt.Errorf("harness compose: %s path %q must be absolute with no colon or comma", label, p)
		}
	}
	return nil
}

func checkSubnets(inner, outer netip.Prefix) error {
	for label, p := range map[string]netip.Prefix{"inner": inner, "outer": outer} {
		if !p.IsValid() || !p.Addr().Is4() || p.Bits() > 29 {
			return fmt.Errorf("harness compose: %s subnet %v must be an IPv4 prefix of /29 or wider", label, p)
		}
	}
	if inner.Overlaps(outer) {
		return fmt.Errorf("harness compose: inner subnet %s overlaps outer subnet %s", inner, outer)
	}
	return nil
}

// relaysFor lists the edge relays: each app port in file order, then each
// postgres service of the scenario in scenario order. A relay is named after
// its port or its service, so the two must not share a name.
func relaysFor(f *File, sc *Scenario, spec RunSpec) ([]Relay, error) {
	var out []Relay
	add := func(name, svc string, port int) error {
		if slices.ContainsFunc(out, func(r Relay) bool { return r.Name == name }) {
			return fmt.Errorf("harness compose: relay name %q is both an app port and a service", name)
		}
		out = append(out, Relay{Name: name, Service: svc, Port: FirstRelayPort + len(out), Target: svc + ":" + strconv.Itoa(port)})
		return nil
	}
	for _, p := range f.App.Ports {
		if err := add(p.Name, ServiceApp, p.Container); err != nil {
			return nil, err
		}
	}
	for _, name := range sc.Services {
		if isPostgres(f.Services[name]) {
			if err := add(name, name, postgresPort); err != nil {
				return nil, err
			}
		}
	}
	if spec.Debug != nil {
		if slices.ContainsFunc(out, func(r Relay) bool { return r.Name == RelayDAP }) {
			return nil, fmt.Errorf("harness compose: relay name %q is reserved for the debugger in a debug run", RelayDAP)
		}
		svc, target := dapTarget(spec)
		out = append(out, Relay{
			Name: RelayDAP, Service: svc, Port: FirstRelayPort + len(out),
			Target: target, Kind: RelayKindDAP,
		})
	}
	return out, nil
}

func isPostgres(s Service) bool {
	return s.Database != nil && s.Database.Kind == "postgres"
}

func dataVolume(service string) string { return "data-" + service }

// disableIPv6 turns IPv6 off inside a container on top of the networks having
// none, so no link-local address or route exists either.
var disableIPv6 = map[string]string{
	"net.ipv6.conf.all.disable_ipv6":     "1",
	"net.ipv6.conf.default.disable_ipv6": "1",
}

func network(subnet netip.Prefix, internal bool) composeNetwork {
	n := composeNetwork{
		Internal:   internal,
		EnableIPv6: false,
		IPAM:       composeIPAM{Config: []composeSubnet{{Subnet: subnet.Masked().String()}}},
	}
	if !internal {
		// The outer network carries only the edge's published relays; without
		// masquerade the edge cannot reach the internet either.
		n.DriverOpts = map[string]string{"com.docker.network.bridge.enable_ip_masquerade": "false"}
	}
	return n
}

func limited(s *composeService, l ResolvedLimits) {
	s.MemLimit = l.Memory
	s.CPUs = l.CPUs
	s.PidsLimit = l.PIDs
}

func appService(f *File, spec RunSpec) composeService {
	s := composeService{
		Image:       spec.AppImage,
		PullPolicy:  pullNever,
		WorkingDir:  "/src",
		User:        fmt.Sprintf("%d:%d", spec.UID, spec.GID),
		ReadOnly:    true,
		CapDrop:     []string{capDropAll},
		SecurityOpt: []string{noNewPrivileges},
		// Docker mounts a tmpfs noexec by default, and go test runs the binary
		// it builds under the temp directory. /src is already writable and
		// executable, so this gives the app nothing new.
		Tmpfs: []string{"/tmp:exec"},
		Environment: map[string]string{
			"HOME":             "/tmp",
			"GOMODCACHE":       "/cache/gomod",
			"GOCACHE":          "/cache/gocache",
			"npm_config_cache": "/cache/npm",
		},
		Volumes: []string{
			spec.Src + ":/src",
			spec.Out + ":/out",
			"gomod:/cache/gomod",
			"gocache:/cache/gocache",
			"npm:/cache/npm",
		},
		Networks:  []string{NetworkInner},
		Sysctls:   disableIPv6,
		Command:   []string{"sh", "-c", f.App.Run},
		DependsOn: map[string]composeCondition{ServiceCacheInit: {Condition: "service_completed_successfully"}},
	}
	limited(&s, f.AppLimits())
	return s
}

// cacheInitService hands the cache volumes to the app's user. Docker creates
// a named volume owned by root, and the app runs unprivileged with no
// capabilities, so it cannot do this itself. It runs only chown, as root with
// CHOWN alone, with no network and without the source mounted.
func cacheInitService(f *File, spec RunSpec) composeService {
	s := composeService{
		Image:       spec.AppImage,
		PullPolicy:  pullNever,
		User:        "0:0",
		ReadOnly:    true,
		CapDrop:     []string{capDropAll},
		CapAdd:      []string{"CHOWN"},
		SecurityOpt: []string{noNewPrivileges},
		NetworkMode: "none",
		Volumes:     []string{"gomod:/cache/gomod", "gocache:/cache/gocache", "npm:/cache/npm"},
		Command:     []string{"chown", fmt.Sprintf("%d:%d", spec.UID, spec.GID), "/cache/gomod", "/cache/gocache", "/cache/npm"},
	}
	limited(&s, f.AppLimits())
	return s
}

// serviceService is the service profile: read-only root, a named data volume
// for postgres, tmpfs /run and /tmp, the image's user unless root is set, and
// only the listed capabilities. no-new-privileges is left off because it
// blocks the setuid drop images such as postgres make at start.
func serviceService(f *File, spec RunSpec, name string) composeService {
	svc := f.Services[name]
	image := svc.Image
	if id, ok := spec.ServiceImages[name]; ok {
		image = id
	}
	caps := slices.Clone(svc.Caps)
	slices.Sort(caps)
	s := composeService{
		Image:       image,
		PullPolicy:  pullNever,
		ReadOnly:    true,
		CapDrop:     []string{capDropAll},
		CapAdd:      caps,
		Tmpfs:       []string{"/run", "/tmp"},
		Environment: map[string]string{},
		Networks:    []string{NetworkInner},
		Sysctls:     disableIPv6,
	}
	if svc.Root {
		s.User = "0:0"
	}
	for k, v := range svc.Env {
		s.Environment[k] = v
	}
	if svc.Ready != "" {
		s.Healthcheck = &composeHealthcheck{
			Test: []string{"CMD-SHELL", svc.Ready}, Interval: "1s", Timeout: "5s", Retries: 60,
		}
	}
	if isPostgres(svc) {
		s.Environment[PostgresReadPasswordEnv] = spec.ReadPassword
		// database.name is the database the image creates, unless the file
		// names one itself.
		if _, ok := s.Environment["POSTGRES_DB"]; !ok && svc.Database.Name != "" {
			s.Environment["POSTGRES_DB"] = svc.Database.Name
		}
		s.Volumes = []string{
			dataVolume(name) + ":" + postgresDataDir,
			"./" + InitDBFile + ":" + postgresInitDir + initDBFile + ":ro",
		}
	}
	if len(s.Environment) == 0 {
		s.Environment = nil
	}
	limited(&s, f.ServiceLimits(name, spec.ServiceMemory))
	return s
}

// edgeService is the edge: aiman's binary, dual-homed, publishing only its
// relay ports on host 127.0.0.1, with forwarding and IPv6 off.
func edgeService(spec RunSpec, relays []Relay) composeService {
	cmd := []string{
		EdgeBinPath, "harness", "edge",
		"--control", ControlMount + "/" + ControlSocketName,
		"--token-file", ControlMount + "/" + ControlTokenName,
		"--outer", spec.OuterSubnet.Masked().String(),
		"--inner", spec.InnerSubnet.Masked().String(),
	}
	if spec.Debug != nil {
		cmd = append(cmd, "--inner", spec.DebugSubnet.Masked().String())
	}
	ports := make([]string, 0, len(relays))
	for _, r := range relays {
		flag := "--relay"
		if r.Kind == RelayKindDAP {
			flag = "--dap-relay"
		}
		cmd = append(cmd, flag, fmt.Sprintf("%s=%d=%s", r.Name, r.Port, r.Target))
		ports = append(ports, fmt.Sprintf("127.0.0.1::%d", r.Port))
	}
	sysctls := map[string]string{
		"net.ipv4.ip_forward":          "0",
		"net.ipv6.conf.all.forwarding": "0",
	}
	for k, v := range disableIPv6 {
		sysctls[k] = v
	}
	s := composeService{
		Image:       spec.EdgeImage,
		PullPolicy:  pullNever,
		User:        fmt.Sprintf("%d:%d", spec.UID, spec.GID),
		ReadOnly:    true,
		CapDrop:     []string{capDropAll},
		SecurityOpt: []string{noNewPrivileges},
		Tmpfs:       []string{"/tmp"},
		Volumes:     []string{spec.AimanBin + ":" + EdgeBinPath + ":ro", spec.ControlDir + ":" + ControlMount},
		Networks:    []string{NetworkInner, NetworkOuter},
		Sysctls:     sysctls,
		Ports:       ports,
		Command:     cmd,
	}
	limited(&s, EdgeLimits)
	return s
}

type composeFile struct {
	Name     string                    `yaml:"name"`
	Services map[string]composeService `yaml:"services"`
	Networks map[string]composeNetwork `yaml:"networks"`
	Volumes  map[string]map[string]any `yaml:"volumes"`
}

type composeService struct {
	Image       string            `yaml:"image"`
	PullPolicy  string            `yaml:"pull_policy"`
	WorkingDir  string            `yaml:"working_dir,omitempty"`
	User        string            `yaml:"user,omitempty"`
	ReadOnly    bool              `yaml:"read_only"`
	CapDrop     []string          `yaml:"cap_drop"`
	CapAdd      []string          `yaml:"cap_add,omitempty"`
	SecurityOpt []string          `yaml:"security_opt,omitempty"`
	Tmpfs       []string          `yaml:"tmpfs,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty"`
	Volumes     []string          `yaml:"volumes,omitempty"`
	MemLimit    int64             `yaml:"mem_limit"`
	CPUs        float64           `yaml:"cpus"`
	PidsLimit   int               `yaml:"pids_limit"`
	Ports       []string          `yaml:"ports,omitempty"`
	NetworkMode string            `yaml:"network_mode,omitempty"`
	// Networks is a []string, or a map of network name to composeAttach
	// when a network needs an alias.
	Networks    any                         `yaml:"networks,omitempty"`
	Sysctls     map[string]string           `yaml:"sysctls,omitempty"`
	Healthcheck *composeHealthcheck         `yaml:"healthcheck,omitempty"`
	Command     []string                    `yaml:"command,omitempty"`
	DependsOn   map[string]composeCondition `yaml:"depends_on,omitempty"`
}

type composeCondition struct {
	Condition string `yaml:"condition"`
}

type composeHealthcheck struct {
	Test     []string `yaml:"test"`
	Interval string   `yaml:"interval"`
	Timeout  string   `yaml:"timeout"`
	Retries  int      `yaml:"retries"`
}

type composeNetwork struct {
	Internal   bool              `yaml:"internal"`
	EnableIPv6 bool              `yaml:"enable_ipv6"`
	DriverOpts map[string]string `yaml:"driver_opts,omitempty"`
	IPAM       composeIPAM       `yaml:"ipam"`
}

type composeIPAM struct {
	Config []composeSubnet `yaml:"config"`
}

type composeSubnet struct {
	Subnet string `yaml:"subnet"`
}
