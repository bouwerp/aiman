package harness

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/bouwerp/aiman/internal/cgroup"
	"github.com/bouwerp/aiman/internal/cigate"
)

// AllowedCaps are the only capabilities a service may add to cap_drop: ALL.
var AllowedCaps = []string{"CHOWN", "SETUID", "SETGID", "DAC_OVERRIDE", "FOWNER"}

// ReservedServiceNames are compose service and host names the harness itself
// uses on the run's networks.
var ReservedServiceNames = []string{"app", "edge", "app-debug", "cache-init", "debug-adapter", "app-check", "debug-check", "debug-check-adapter"}

var (
	// A service name is a DNS label: it is a compose service and a host name.
	serviceNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	portNameRe    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,30}$`)
	scenarioIDRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	envNameRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	placeholderRe = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)
	imageCharsRe  = regexp.MustCompile(`^[A-Za-z0-9._/:@-]+$`)
	stubHostRe    = regexp.MustCompile(`^[A-Za-z0-9.-]+(:\d+)?$`)
	validDBKinds  = []string{"postgres", "sqlite"}
)

// Validate returns every problem in f, in file order.
func (f *File) Validate() []string {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	if f.Version != FileVersion {
		add("version must be %d, got %d", FileVersion, f.Version)
	}
	errs = append(errs, f.App.validate()...)
	// Edge relays are named after app ports and services alike.
	for i, p := range f.App.Ports {
		if _, ok := f.Services[p.Name]; ok {
			add("app.ports[%d]: name %q is also a service; relay names must be unique", i, p.Name)
		}
	}
	for _, name := range sortedKeys(f.Services) {
		errs = append(errs, validateService(name, f.Services[name])...)
	}
	if len(f.Scenarios) == 0 {
		add("scenarios: at least one scenario is required")
	}
	seen := map[string]bool{}
	for i := range f.Scenarios {
		sc := &f.Scenarios[i]
		if sc.ID != "" && seen[sc.ID] {
			add("scenarios[%d]: duplicate id %q", i, sc.ID)
		}
		seen[sc.ID] = true
		errs = append(errs, f.validateScenario(i, sc)...)
	}
	errs = append(errs, f.validateAWSIdentity()...)
	return errs
}

func (a *App) validate() []string {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	if a.Run == "" {
		add("app.run is required")
	}
	names := map[string]bool{}
	for i, p := range a.Ports {
		if !portNameRe.MatchString(p.Name) {
			add("app.ports[%d]: name %q must match %s", i, p.Name, portNameRe)
		}
		if names[p.Name] {
			add("app.ports[%d]: duplicate name %q", i, p.Name)
		}
		names[p.Name] = true
		if p.Container < 1 || p.Container > 65535 {
			add("app.ports[%d]: container port %d is not 1-65535", i, p.Container)
		}
	}
	errs = append(errs, validateLimits("app.limits", a.Limits)...)
	if a.Image != "" && !imageCharsRe.MatchString(a.Image) {
		add("app.image %q must be a plain image reference", a.Image)
	}
	if a.Debug != nil {
		errs = append(errs, a.Debug.validate("app.debug", false)...)
	}
	return errs
}

func validateService(name string, s Service) []string {
	var errs []string
	at := "services." + name
	add := func(format string, args ...any) { errs = append(errs, at+": "+fmt.Sprintf(format, args...)) }
	if !serviceNameRe.MatchString(name) {
		add("service name must match %s", serviceNameRe)
	}
	if slices.Contains(ReservedServiceNames, name) {
		add("the name is reserved")
	}
	if strings.HasSuffix(name, awsEmuSuffix) || strings.HasSuffix(name, awsInitSuffix) || strings.HasSuffix(name, awsApplySuffix) {
		add("names ending %s, %s or %s are reserved for aws emulators", awsEmuSuffix, awsInitSuffix, awsApplySuffix)
	}
	if s.AWS != nil {
		return append(errs, validateAWS(at, s)...)
	}
	if s.Image == "" || !imageCharsRe.MatchString(s.Image) {
		add("image %q must be a plain image reference", s.Image)
	}
	for k := range s.Env {
		if !envNameRe.MatchString(k) {
			add("env name %q is not a variable name", k)
		}
	}
	if s.Database != nil {
		for _, e := range validateDatabase(*s.Database) {
			add("%s", e)
		}
	}
	for _, c := range s.Caps {
		if !slices.Contains(AllowedCaps, c) {
			add("cap %s is not allowed; only %s", c, strings.Join(AllowedCaps, ", "))
		}
	}
	errs = append(errs, validateLimits(at+".limits", s.Limits)...)
	errs = append(errs, validateEVM(at, s)...)
	return errs
}

// sqliteDirsRefused are directories a SQLite database.path may not lie in:
// the service's tmpfs mounts and the kernel's.
var sqliteDirsRefused = []string{"run", "tmp", "proc", "sys", "dev"}

// validateDatabase checks a service's database. A postgres one names no
// path. A SQLite one names a clean absolute file below a directory of its
// own, since that directory becomes the service's data volume.
func validateDatabase(d Database) []string {
	switch d.Kind {
	case "postgres":
		if d.Path != "" {
			return []string{"database.path is for sqlite; postgres takes name"}
		}
	case "sqlite":
		p := d.Path
		switch {
		case p == "":
			return []string{"database.path is required for sqlite: the database file inside the service, such as /data/app.db"}
		case !path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, ":\\\n"):
			return []string{fmt.Sprintf("database.path %q must be a clean absolute path", p)}
		case path.Dir(p) == "/" || slices.Contains(sqliteDirsRefused, strings.Split(p, "/")[1]):
			return []string{fmt.Sprintf("database.path %q must lie in a directory of its own, not / or /%s", p, strings.Join(sqliteDirsRefused, ", /"))}
		case d.Name != "":
			return []string{"database.name is for postgres; sqlite takes path"}
		}
	default:
		return []string{fmt.Sprintf("database.kind %q must be one of %s", d.Kind, strings.Join(validDBKinds, ", "))}
	}
	return nil
}

func validateLimits(at string, l Limits) []string {
	var errs []string
	if l.Memory != "" {
		n, err := cgroup.ParseSize(l.Memory)
		if err != nil || n == cgroup.Infinity {
			errs = append(errs, fmt.Sprintf("%s.memory %q must be a finite size such as 2g or 512m", at, l.Memory))
		}
	}
	if l.CPUs < 0 {
		errs = append(errs, fmt.Sprintf("%s.cpus must not be negative", at))
	}
	if l.PIDs < 0 {
		errs = append(errs, fmt.Sprintf("%s.pids must not be negative", at))
	}
	return errs
}

func (f *File) validateScenario(i int, sc *Scenario) []string {
	var errs []string
	at := fmt.Sprintf("scenarios[%d]", i)
	if sc.ID != "" {
		at = fmt.Sprintf("scenarios[%d] %s", i, sc.ID)
	}
	add := func(format string, args ...any) { errs = append(errs, at+": "+fmt.Sprintf(format, args...)) }
	if !scenarioIDRe.MatchString(sc.ID) {
		add("id %q must match %s", sc.ID, scenarioIDRe)
	}
	for _, name := range sc.Services {
		if _, ok := f.Services[name]; !ok {
			add("service %q is not declared under services", name)
		}
	}
	for j, s := range sc.Setup {
		if strings.TrimSpace(s.Run) == "" {
			add("setup[%d].run is required", j)
		}
	}
	for j, fx := range sc.Fixtures {
		for _, e := range validateFixture(fx, sc.Services) {
			add("fixtures[%d]: %s", j, e)
		}
	}
	errs = append(errs, f.validateSteps(at, sc.Steps)...)
	checks := map[string]bool{}
	for j, c := range sc.Checks {
		if strings.TrimSpace(c.Name) == "" {
			add("checks[%d].name is required", j)
		} else if checks[c.Name] {
			add("checks[%d]: duplicate name %q", j, c.Name)
		}
		checks[c.Name] = true
		if strings.TrimSpace(c.Run) == "" {
			add("checks[%d].run is required", j)
		}
		if c.Image != "" && !imageCharsRe.MatchString(c.Image) {
			add("checks[%d].image %q must be a plain image reference", j, c.Image)
		}
		if c.Debug != nil {
			for _, e := range c.Debug.validate(fmt.Sprintf("checks[%d].debug", j), true) {
				add("%s", e)
			}
		}
	}
	for j, st := range sc.Stubs {
		for _, e := range validateStub(st) {
			add("stubs[%d]: %s", j, e)
		}
	}
	for _, e := range f.validateScenarioAWS(sc) {
		add("%s", e)
	}
	return errs
}

func (f *File) validateSteps(at string, steps []Step) []string {
	var errs []string
	ports := map[string]bool{}
	for _, p := range f.App.Ports {
		ports[p.Name] = true
	}
	for j, st := range steps {
		if strings.TrimSpace(st.Do) == "" {
			errs = append(errs, fmt.Sprintf("%s: steps[%d].do is required", at, j))
		}
		for _, text := range []string{st.Do, st.Expect} {
			for _, m := range placeholderRe.FindAllStringSubmatch(text, -1) {
				if !ports[m[1]] {
					errs = append(errs, fmt.Sprintf("%s: steps[%d]: {{%s}} names no app port", at, j, m[1]))
				}
			}
		}
	}
	return errs
}

func validateFixture(fx Fixture, services []string) []string {
	var errs []string
	set := 0
	for _, p := range []string{fx.SQL, fx.SQLite, fx.Script} {
		if p == "" {
			continue
		}
		set++
		if err := cigate.ValidateDir(p); err != nil {
			errs = append(errs, fmt.Sprintf("path %q %v", p, err))
		}
	}
	if set != 1 {
		errs = append(errs, "set exactly one of sql, sqlite or script")
	}
	needsInto := fx.SQL != "" || fx.SQLite != ""
	switch {
	case fx.Into == "" && needsInto:
		errs = append(errs, "into is required for sql and sqlite")
	case fx.Into != "" && !slices.Contains(services, fx.Into):
		errs = append(errs, fmt.Sprintf("into %q is not one of the scenario's services", fx.Into))
	}
	return errs
}

func validateStub(st Stub) []string {
	var errs []string
	if !stubHostRe.MatchString(st.Host) {
		errs = append(errs, fmt.Sprintf("host %q is not a host name", st.Host))
	}
	if !strings.HasPrefix(st.Path, "/") {
		errs = append(errs, fmt.Sprintf("path %q must start with /", st.Path))
	}
	if st.Status != 0 && (st.Status < 100 || st.Status > 599) {
		errs = append(errs, fmt.Sprintf("status %d is not 100-599", st.Status))
	}
	if st.Body != "" && st.BodyFile != "" {
		errs = append(errs, "set body or body_file, not both")
	}
	if st.BodyFile != "" {
		if err := cigate.ValidateDir(st.BodyFile); err != nil {
			errs = append(errs, fmt.Sprintf("body_file %q %v", st.BodyFile, err))
		}
	}
	return errs
}

// Paths returns the repository paths the file names: fixture and stub body
// files, and the first word of each setup and check command when it is a
// relative path with a directory (scripts/harness/migrate.sh), and the aws
// stacks, var files and seed files. Duplicates are
// dropped; paths are cleaned.
func (f *File) Paths() []string {
	seen := map[string]bool{}
	var out []string
	addPath := func(p string) {
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	addCmd := func(cmd string) {
		fields := strings.Fields(cmd)
		if len(fields) == 0 {
			return
		}
		w := fields[0]
		if strings.Contains(w, "/") && !filepath.IsAbs(w) && cigate.ValidateDir(w) == nil {
			addPath(w)
		}
	}
	for _, name := range sortedKeys(f.Services) {
		a := f.Services[name].AWS
		if a == nil {
			continue
		}
		for _, st := range a.Stacks {
			addPath(st.CloudFormation)
			addPath(st.Terraform)
			for _, v := range st.VarFiles {
				addPath(v)
			}
		}
		addPath(a.Seed)
	}
	for _, sc := range f.Scenarios {
		for _, name := range sortedKeys(sc.AWS) {
			addPath(sc.AWS[name].Seed)
		}
		for _, s := range sc.Setup {
			addCmd(s.Run)
		}
		for _, fx := range sc.Fixtures {
			addPath(fx.SQL)
			addPath(fx.SQLite)
			addPath(fx.Script)
		}
		for _, c := range sc.Checks {
			addCmd(c.Run)
		}
		for _, st := range sc.Stubs {
			addPath(st.BodyFile)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
