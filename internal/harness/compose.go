package harness

import (
	"embed"
	"errors"
	"fmt"
	"net/netip"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bouwerp/aiman/internal/sandbox"
)

// Compose service, network and mount names of a run's project.
const (
	ServiceApp       = "app"
	ServiceEdge      = "edge"
	ServiceCacheInit = sandbox.CacheInitService
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

// Hardening values from internal/sandbox.
const (
	noNewPrivileges = sandbox.NoNewPrivileges
	capDropAll      = sandbox.CapDropAll
	pullNever       = sandbox.PullNever
)

// appCaches are the app's cache volumes.
var appCaches = sandbox.Caches{GoMod: "gomod", GoCache: "gocache", NPM: "npm"}

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
	// DebugChecks gives a run whose app runs plainly the debug network,
	// so its checks can be debugged.
	DebugChecks bool
	// CheckDebug adds the services of one check under its debugger
	// (ServiceDebugCheck, and ServiceCheckAdapter run by
	// CheckAdapterImage when it has an Adapter).
	CheckDebug        *DebugSpec
	CheckAdapterImage string
	// Proxy turns on the edge's capture proxy (compose_proxy.go); nil runs
	// none.
	Proxy *ProxySpec
	// CheckImages runs each check that names an image, keyed by
	// CheckService of the check's index (serve passes image IDs).
	CheckImages map[string]string
	// CheckDebugImage runs the debug check when its check names an image.
	CheckDebugImage string
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
		Volumes: appCaches.Volumes(),
	}
	app, edge := appService(f, spec), edgeService(spec, relays)
	addDebugServices(&doc, f, spec, &app, &edge)
	for j, c := range sc.Checks {
		if c.Image != "" {
			doc.Services[CheckService(j)] = checkService(f, spec, spec.CheckImages[CheckService(j)])
		}
	}
	doc.Services[ServiceApp] = app
	doc.Services[ServiceCacheInit] = cacheInitService(f, spec)
	doc.Services[ServiceEdge] = edge
	files := map[string][]byte{}
	for _, name := range sc.Services {
		if f.Services[name].AWS != nil {
			addAWSService(&doc, f, spec, name)
			continue
		}
		doc.Services[name] = serviceService(f, spec, name)
		if isPostgres(f.Services[name]) {
			doc.Volumes[dataVolume(name)] = map[string]any{}
			files[InitDBFile] = PostgresInitSQL()
		}
		if isSQLite(f.Services[name]) {
			doc.Volumes[dataVolume(name)] = map[string]any{}
		}
	}
	if err := withProxy(&doc, f, sc, spec); err != nil {
		return nil, err
	}
	withAWS(&doc, files, f, sc)
	for name, svc := range doc.Services {
		doc.Services[name] = sandbox.EscapeInterpolation(svc)
	}
	raw, err := sandbox.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return &Project{Name: doc.Name, YAML: raw, Relays: relays, Files: files}, nil
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
	for j, c := range sc.Checks {
		if c.Image != "" && spec.CheckImages[CheckService(j)] == "" {
			return nil, fmt.Errorf("harness compose: check %q needs its image ID", c.Name)
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
		if e := f.Services[name].EVM; e != nil {
			if err := add(name, name, e.port()); err != nil {
				return nil, err
			}
			out[len(out)-1].Kind = RelayKindEVM
		}
	}
	if spec.debugNet() {
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

func isSQLite(s Service) bool {
	return s.Database != nil && s.Database.Kind == "sqlite"
}

func dataVolume(service string) string { return "data-" + service }

// DataVolumeName is the Docker name of a service's data volume in run's
// compose project: compose prefixes the project name.
func DataVolumeName(run, service string) string {
	return "harness-" + run + "_" + dataVolume(service)
}

// disableIPv6 is sandbox.DisableIPv6.
var disableIPv6 = sandbox.DisableIPv6

// network is the run network on subnet (sandbox.NetworkOf). The outer
// network carries only the edge's published relays.
func network(subnet netip.Prefix, internal bool) composeNetwork {
	return sandbox.NetworkOf(subnet, internal)
}

func limited(s *composeService, l ResolvedLimits) {
	sandbox.Limit(s, sandbox.MemoryBytes(l.Memory), l.CPUs, l.PIDs)
}

func appService(f *File, spec RunSpec) composeService {
	s := sandbox.Workload(spec.AppImage, spec.UID, spec.GID, spec.Src, spec.Out, appCaches)
	s.PullPolicy = pullNever
	s.Networks = []string{NetworkInner}
	s.Sysctls = disableIPv6
	s.Command = []string{"sh", "-c", f.App.Run}
	limited(&s, f.AppLimits())
	return s
}

// checksProfile holds the check image services, which only harness.check
// starts.
const checksProfile = "checks"

// CheckService is the compose service of the scenario's check at index j
// when that check names its own image. The underscore keeps it apart from
// every service name a harness file may use.
func CheckService(j int) string { return "check_" + strconv.Itoa(j) }

// checkService runs a check in its own image: the app's hardening, mounts,
// env, limits and inner network, with no command of its own (harness.check
// passes the check's). The checks profile keeps compose up from starting it.
func checkService(f *File, spec RunSpec, image string) composeService {
	s := appService(f, spec)
	s.Image = image
	s.Command = nil
	s.Profiles = []string{checksProfile}
	return s
}

// cacheInitService hands the cache volumes to the app's user
// (sandbox.CacheInit).
func cacheInitService(f *File, spec RunSpec) composeService {
	s := sandbox.CacheInit(spec.AppImage, spec.UID, spec.GID, appCaches)
	s.PullPolicy = pullNever
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
	if svc.EVM != nil {
		evmService(&s, svc)
	}
	if isSQLite(svc) {
		// The file's directory is a named volume, writable under the
		// read-only root, which the database browser mounts read-only into
		// its reader container.
		s.Volumes = []string{dataVolume(name) + ":" + path.Dir(svc.Database.Path)}
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
	if spec.debugNet() {
		cmd = append(cmd, "--inner", spec.DebugSubnet.Masked().String())
	}
	ports := make([]string, 0, len(relays))
	for _, r := range relays {
		flag := "--relay"
		switch r.Kind {
		case RelayKindDAP:
			flag = "--dap-relay"
		case RelayKindEVM:
			flag = "--evm-relay"
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

// The compose document types are internal/sandbox's.
type (
	composeFile        = sandbox.File
	composeService     = sandbox.Service
	composeHealthcheck = sandbox.Healthcheck
	composeNetwork     = sandbox.Network
	composeAttach      = sandbox.Attach
	composeCondition   = sandbox.Condition
)
