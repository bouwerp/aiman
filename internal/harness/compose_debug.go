package harness

import (
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Debug runs. The app joins a third network, debug, beside inner. Only the
// app, the edge, a check under the debugger and, for languages whose DAP
// server does not run in the target, an adapter container are on it: a
// service cannot reach the debug port or an inspector, so a hostile service
// image cannot attach and evaluate code in the app. Every container on it
// has a fixed address (DebugAddrs, DebugCheckAddrs), because Node's
// inspector refuses a Host header that is neither an IP address nor
// localhost. The edge relays the DAP server (in the target or its adapter)
// under a lease.
const (
	NetworkDebug = "debug"
	// ServiceAdapter is the adapter container of a debug run whose DAP
	// server runs outside the app (js-debug for Node).
	ServiceAdapter = "debug-adapter"
	// ServiceCheck runs the plain checks of a debug run: the app's
	// container without the debug network, whose fixed address the app
	// holds.
	ServiceCheck = "app-check"
	// ServiceDebugCheck runs one check under its debugger at the check's
	// fixed address, and ServiceCheckAdapter that check's adapter
	// container when its language has one.
	ServiceDebugCheck   = "debug-check"
	ServiceCheckAdapter = "debug-check-adapter"
	// RelayDAP names the debugger's relay and its forwarded port.
	RelayDAP = "dap"
	// RelayKindDAP marks a relay that holds DAP clients under a lease.
	RelayKindDAP = "dap"
	// ToolsMount is where debug tools are mounted read-only in the app.
	ToolsMount = "/aiman-tools"
	// InspectEnv carries the target's debug address and the debugger's
	// port there, <ip>:<port>, into the target container, followed by
	// /<DebugSpec.InspectPath> when the spec has one. A debug command
	// listens on it.
	InspectEnv = "AIMAN_INSPECT"
	// AdapterHostEnv is the adapter container's own debug address, which its
	// DAP server listens on.
	AdapterHostEnv = "AIMAN_DAP_HOST"
)

// Compose profiles keep the check services out of every compose up that
// does not name them.
const (
	profileCheck      = "check"
	profileDebugCheck = "debug-check"
)

// AdapterLimits are the adapter container's fixed limits.
var AdapterLimits = ResolvedLimits{Memory: 512 << 20, CPUs: 1, PIDs: 128}

// DebugAddrs are the fixed addresses on a debug subnet: .2 the app (the
// debug target), .3 the adapter container, .4 the edge.
func DebugAddrs(subnet netip.Prefix) (app, adapter, edge netip.Addr) {
	base := subnet.Masked().Addr()
	app = base.Next().Next()
	adapter = app.Next()
	edge = adapter.Next()
	return app, adapter, edge
}

// DebugCheckAddrs are a debug check's fixed addresses: .5 the check
// container, .6 its adapter container. The app keeps .2 while a check is
// debugged.
func DebugCheckAddrs(subnet netip.Prefix) (check, adapter netip.Addr) {
	_, _, edge := DebugAddrs(subnet)
	check = edge.Next()
	return check, check.Next()
}

// DebugNet is a run's debug addresses: the target (App) and its adapter as
// a DebugAdapter's Launch sees them, and the edge.
type DebugNet struct {
	App, Adapter, Edge netip.Addr
}

// DebugNetOf is the debug addresses of subnet with the app as the target.
func DebugNetOf(subnet netip.Prefix) DebugNet {
	app, adapter, edge := DebugAddrs(subnet)
	return DebugNet{App: app, Adapter: adapter, Edge: edge}
}

// DebugCheckNetOf is the debug addresses of subnet with a debug check as
// the target.
func DebugCheckNetOf(subnet netip.Prefix) DebugNet {
	_, _, edge := DebugAddrs(subnet)
	check, adapter := DebugCheckAddrs(subnet)
	return DebugNet{App: check, Adapter: adapter, Edge: edge}
}

// DebugSpec is how one debug target starts: the app of a debug run, or a
// check under the debugger. A DebugAdapter makes it; Compose only places
// it, so the compose builder knows no debugger.
type DebugSpec struct {
	// Lang is the debug block's lang the adapter serves.
	Lang string
	// Command replaces app.run (or the check's run). The debugger in the
	// target (dlv, debugpy, or the Node or Bun inspector) listens on
	// $AIMAN_INSPECT.
	Command []string
	// Env joins the target's environment.
	Env map[string]string
	// Mounts are host tools the target needs, mounted read-only.
	Mounts []ToolMount
	// Port is the debugger's port on the target's debug address: the DAP
	// server itself, or the inspector the adapter attaches to.
	Port int
	// Adapter runs the DAP server in its own container; nil when the
	// target runs it.
	Adapter *AdapterSpec
	// InspectPath is the inspector's URL path in $AIMAN_INSPECT, without
	// the leading slash: Bun's random per-run prefix. Empty for the others.
	InspectPath string
	// Probe, when set, runs in the target's container before the target
	// starts, and may refuse it.
	Probe *DebugProbe
}

// DebugProbe checks the app container before a debug run's app starts: the
// runtime's version, for example. Command runs in the app container with
// the run's mounts and environment; Check reads its output and refuses the
// run with its error.
type DebugProbe struct {
	Command []string
	Check   func(out string) error
}

// AdapterSpec is the adapter container of a debug run. It runs only on the
// debug network at the adapter's address, with the app profile, the run's
// src and out mounted read-only at the app's paths and its tools read-only.
type AdapterSpec struct {
	// Command runs the DAP server on $AIMAN_DAP_HOST:Port.
	Command []string
	Mounts  []ToolMount
	Port    int
}

// ToolMount is one read-only bind mount of a host file or directory.
type ToolMount struct {
	Host      string
	Container string
}

// debugNet reports whether the run has a debug network: a debug run of the
// app, or one that debugs only its checks.
func (spec RunSpec) debugNet() bool { return spec.Debug != nil || spec.DebugChecks }

// checkDebug refuses a debug spec the file did not ask for, a spec for
// another language, an unusable mount and an overlapping debug subnet.
func checkDebug(f *File, spec RunSpec) error {
	if !spec.debugNet() {
		if spec.CheckDebug != nil {
			return errors.New("harness compose: a debug check needs a debug run")
		}
		return nil
	}
	if d := spec.Debug; d != nil {
		if f.App.Debug == nil {
			return errors.New("harness compose: a debug run needs app.debug in the harness file")
		}
		if f.App.Debug.Lang != d.Lang {
			return fmt.Errorf("harness compose: the file's app.debug.lang is %q but the debugger is for %q", f.App.Debug.Lang, d.Lang)
		}
		if err := checkDebugSpec(d, spec.AdapterImage); err != nil {
			return err
		}
	}
	if c := spec.CheckDebug; c != nil {
		if err := checkDebugSpec(c, spec.CheckAdapterImage); err != nil {
			return err
		}
	}
	p := spec.DebugSubnet
	if !p.IsValid() || !p.Addr().Is4() || p.Bits() > 29 {
		return fmt.Errorf("harness compose: debug subnet %v must be an IPv4 prefix of /29 or wider", p)
	}
	if p.Overlaps(spec.InnerSubnet) || p.Overlaps(spec.OuterSubnet) {
		return fmt.Errorf("harness compose: debug subnet %s overlaps the inner or outer subnet", p)
	}
	return nil
}

// checkDebugSpec refuses a target spec with no command or port, an adapter
// with no image, command or port, an unusable mount and a bad inspector
// path.
func checkDebugSpec(d *DebugSpec, adapterImage string) error {
	if len(d.Command) == 0 || !validPort(d.Port) {
		return errors.New("harness compose: the debug spec needs a command and a port")
	}
	mounts := d.Mounts
	if a := d.Adapter; a != nil {
		if len(a.Command) == 0 || !validPort(a.Port) {
			return errors.New("harness compose: the adapter needs a command and a port")
		}
		if adapterImage == "" {
			return errors.New("harness compose: the debug adapter needs an image")
		}
		mounts = append(append([]ToolMount(nil), mounts...), a.Mounts...)
	}
	for _, m := range mounts {
		if err := checkToolMount(m); err != nil {
			return err
		}
	}
	if d.InspectPath != "" && !inspectPathRe.MatchString(d.InspectPath) {
		return fmt.Errorf("harness compose: inspector path %q must be letters, digits, - and _", d.InspectPath)
	}
	return nil
}

var inspectPathRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func validPort(p int) bool { return p >= 1 && p <= 65535 }

func checkToolMount(m ToolMount) error {
	if !filepath.IsAbs(m.Host) || strings.ContainsAny(m.Host, ":,\n") {
		return fmt.Errorf("harness compose: debug tool path %q must be absolute with no colon or comma", m.Host)
	}
	if !strings.HasPrefix(m.Container, ToolsMount+"/") || strings.ContainsAny(m.Container, ":,\n") {
		return fmt.Errorf("harness compose: debug tool mount %q must lie under %s", m.Container, ToolsMount)
	}
	return nil
}

// inspectValue is $AIMAN_INSPECT for d at addr.
func inspectValue(d *DebugSpec, addr netip.Addr) string {
	v := addr.String() + ":" + strconv.Itoa(d.Port)
	if d.InspectPath != "" {
		v += "/" + d.InspectPath
	}
	return v
}

// dapAddr is what the edge's DAP relay dials for d with the target at
// target and its adapter at adapter: the adapter container when d has one,
// else the DAP server in the target.
func dapAddr(d *DebugSpec, target, adapter netip.Addr) string {
	if a := d.Adapter; a != nil {
		return adapter.String() + ":" + strconv.Itoa(a.Port)
	}
	return target.String() + ":" + strconv.Itoa(d.Port)
}

// dapTarget is the service and address the DAP relay dials at the start:
// the app's debugger, or nothing in a run that debugs only its checks.
func dapTarget(spec RunSpec) (service, addr string) {
	d := spec.Debug
	if d == nil {
		return ServiceDebugCheck, ""
	}
	app, ad, _ := DebugAddrs(spec.DebugSubnet)
	if d.Adapter != nil {
		return ServiceAdapter, dapAddr(d, app, ad)
	}
	return ServiceApp, dapAddr(d, app, ad)
}

// AppDAPTarget is where the DAP relay points while the app is the debug
// target: its debugger or adapter, or empty in a run that debugs only its
// checks.
func AppDAPTarget(spec RunSpec) string {
	_, addr := dapTarget(spec)
	return addr
}

// CheckDAPTarget is where the DAP relay points while spec.CheckDebug's
// check is the debug target.
func CheckDAPTarget(spec RunSpec) string {
	check, adapter := DebugCheckAddrs(spec.DebugSubnet)
	return dapAddr(spec.CheckDebug, check, adapter)
}

// debugApp puts the app on the debug network at its fixed address under
// spec.Debug.
func debugApp(s *composeService, spec RunSpec) {
	app, _, _ := DebugAddrs(spec.DebugSubnet)
	debugTarget(s, spec.Debug, app)
}

// debugTarget puts a target container on the debug network at addr, mounts
// the adapter's tools read-only, adds its environment and AIMAN_INSPECT, and
// replaces its command.
func debugTarget(s *composeService, d *DebugSpec, addr netip.Addr) {
	s.Networks = map[string]composeAttach{
		NetworkInner: {},
		NetworkDebug: {IPv4Address: addr.String()},
	}
	for _, m := range d.Mounts {
		s.Volumes = append(s.Volumes, m.Host+":"+m.Container+":ro")
	}
	if s.Environment == nil {
		s.Environment = map[string]string{}
	}
	maps.Copy(s.Environment, d.Env)
	s.Environment[InspectEnv] = inspectValue(d, addr)
	s.Command = append([]string(nil), d.Command...)
}

// appCheckService runs a debug run's plain checks: the app's container on the
// inner network only, started only by name.
func appCheckService(f *File, spec RunSpec) composeService {
	s := appService(f, spec)
	s.Profiles = []string{profileCheck}
	return s
}

// debugCheckService runs one check under spec.CheckDebug at the check's
// fixed address. It is started by name, runs once and stays until serve
// removes it, so its exit code and output can be read.
func debugCheckService(f *File, spec RunSpec) composeService {
	s := appService(f, spec)
	if spec.CheckDebugImage != "" {
		s.Image = spec.CheckDebugImage
	}
	check, _ := DebugCheckAddrs(spec.DebugSubnet)
	debugTarget(&s, spec.CheckDebug, check)
	s.Profiles = []string{profileDebugCheck}
	s.Restart = "no"
	return s
}

// debugEdge adds the debug network, at the edge's fixed address.
func debugEdge(s *composeService, spec RunSpec) {
	_, _, edge := DebugAddrs(spec.DebugSubnet)
	s.Networks = map[string]composeAttach{
		NetworkInner: {},
		NetworkOuter: {},
		NetworkDebug: {IPv4Address: edge.String()},
	}
}

// adapterService is an adapter container at addr: aiman's pinned glibc
// image with the tools, src and out read-only, on the debug network alone,
// under the app's hardening and small limits. It reaches the target's
// inspector and nothing else; the edge reaches its DAP port.
func adapterService(spec RunSpec, a *AdapterSpec, image string, addr netip.Addr) composeService {
	vols := make([]string, 0, len(a.Mounts)+2)
	for _, m := range a.Mounts {
		vols = append(vols, m.Host+":"+m.Container+":ro")
	}
	vols = append(vols, spec.Src+":/src:ro", spec.Out+":/out:ro")
	s := composeService{
		Image:       image,
		PullPolicy:  pullNever,
		WorkingDir:  "/src",
		User:        fmt.Sprintf("%d:%d", spec.UID, spec.GID),
		ReadOnly:    true,
		CapDrop:     []string{capDropAll},
		SecurityOpt: []string{noNewPrivileges},
		Tmpfs:       []string{"/tmp"},
		Environment: map[string]string{"HOME": "/tmp", AdapterHostEnv: addr.String()},
		Volumes:     vols,
		Networks:    map[string]composeAttach{NetworkDebug: {IPv4Address: addr.String()}},
		Sysctls:     disableIPv6,
		Command:     append([]string(nil), a.Command...),
	}
	limited(&s, AdapterLimits)
	return s
}

// addDebugServices places the debug network's services in doc: the app
// under its debugger, the adapters, the check services and the edge's
// debug address.
func addDebugServices(doc *composeFile, f *File, spec RunSpec, app, edge *composeService) {
	if !spec.debugNet() {
		return
	}
	doc.Networks[NetworkDebug] = network(spec.DebugSubnet, true)
	debugEdge(edge, spec)
	_, ad, _ := DebugAddrs(spec.DebugSubnet)
	if d := spec.Debug; d != nil {
		debugApp(app, spec)
		if d.Adapter != nil {
			doc.Services[ServiceAdapter] = adapterService(spec, d.Adapter, spec.AdapterImage, ad)
		}
	}
	doc.Services[ServiceCheck] = appCheckService(f, spec)
	if c := spec.CheckDebug; c != nil {
		doc.Services[ServiceDebugCheck] = debugCheckService(f, spec)
		if c.Adapter != nil {
			_, cad := DebugCheckAddrs(spec.DebugSubnet)
			s := adapterService(spec, c.Adapter, spec.CheckAdapterImage, cad)
			s.Profiles = []string{profileDebugCheck}
			doc.Services[ServiceCheckAdapter] = s
		}
	}
}
