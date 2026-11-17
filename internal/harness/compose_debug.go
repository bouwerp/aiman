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
// app, the edge and, for languages whose DAP server does not run in the app,
// the adapter container are on it: a service cannot reach the debug port or
// an inspector, so a hostile service image cannot attach and evaluate code in
// the app. Every container on it has a fixed address (DebugAddrs), because
// Node's inspector refuses a Host header that is neither an IP address nor
// localhost. The edge relays the DAP server (in the app or the adapter) under
// a lease.
const (
	NetworkDebug = "debug"
	// ServiceAdapter is the adapter container of a debug run whose DAP
	// server runs outside the app (js-debug for Node).
	ServiceAdapter = "debug-adapter"
	// RelayDAP names the debugger's relay and its forwarded port.
	RelayDAP = "dap"
	// RelayKindDAP marks a relay that holds DAP clients under a lease.
	RelayKindDAP = "dap"
	// ToolsMount is where debug tools are mounted read-only in the app.
	ToolsMount = "/aiman-tools"
	// InspectEnv carries the app's debug address and the debugger's port in
	// the app, <ip>:<port>, into the app container, followed by
	// /<DebugSpec.InspectPath> when the spec has one. A debug command
	// listens on it.
	InspectEnv = "AIMAN_INSPECT"
	// AdapterHostEnv is the adapter container's own debug address, which its
	// DAP server listens on.
	AdapterHostEnv = "AIMAN_DAP_HOST"
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

// DebugNet is a run's debug addresses.
type DebugNet struct {
	App, Adapter, Edge netip.Addr
}

// DebugNetOf is the debug addresses of subnet.
func DebugNetOf(subnet netip.Prefix) DebugNet {
	app, adapter, edge := DebugAddrs(subnet)
	return DebugNet{App: app, Adapter: adapter, Edge: edge}
}

// DebugSpec is how one debug run starts its app. A DebugAdapter makes it;
// Compose only places it, so the compose builder knows no debugger.
type DebugSpec struct {
	// Lang is the file's app.debug.lang the adapter serves.
	Lang string
	// Command replaces app.run. The debugger in the app (dlv, or the Node
	// inspector) listens on $AIMAN_INSPECT.
	Command []string
	// Env joins the app's environment for the build and the app.
	Env map[string]string
	// Mounts are host tools the app needs, mounted read-only.
	Mounts []ToolMount
	// Port is the debugger's port on the app's debug address: the DAP server
	// itself, or the inspector the adapter attaches to.
	Port int
	// Adapter runs the DAP server in its own container; nil when the app
	// runs it.
	Adapter *AdapterSpec
	// InspectPath is the inspector's URL path in $AIMAN_INSPECT, without
	// the leading slash: Bun's random per-run prefix. Empty for the others.
	InspectPath string
	// Probe, when set, runs in the app container after the build and
	// before the app starts, and may refuse the run.
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

// checkDebug refuses a debug spec the file did not ask for, a spec for
// another language, an unusable mount and an overlapping debug subnet.
func checkDebug(f *File, spec RunSpec) error {
	d := spec.Debug
	if d == nil {
		return nil
	}
	if f.App.Debug == nil {
		return errors.New("harness compose: a debug run needs app.debug in the harness file")
	}
	if f.App.Debug.Lang != d.Lang {
		return fmt.Errorf("harness compose: the file's app.debug.lang is %q but the debugger is for %q", f.App.Debug.Lang, d.Lang)
	}
	if len(d.Command) == 0 || !validPort(d.Port) {
		return errors.New("harness compose: the debug spec needs a command and a port")
	}
	mounts := d.Mounts
	if a := d.Adapter; a != nil {
		if len(a.Command) == 0 || !validPort(a.Port) {
			return errors.New("harness compose: the adapter needs a command and a port")
		}
		if spec.AdapterImage == "" {
			return errors.New("harness compose: the debug adapter needs an image")
		}
		mounts = append(append([]ToolMount(nil), mounts...), a.Mounts...)
	}
	for _, m := range mounts {
		if err := checkToolMount(m); err != nil {
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

// inspectAddr is the app's debug address and the debugger's port there.
func inspectAddr(spec RunSpec) string {
	app, _, _ := DebugAddrs(spec.DebugSubnet)
	return app.String() + ":" + strconv.Itoa(spec.Debug.Port)
}

// dapTarget is what the edge's DAP relay dials: the adapter container when
// the run has one, else the DAP server in the app.
func dapTarget(spec RunSpec) (service, addr string) {
	if a := spec.Debug.Adapter; a != nil {
		_, ad, _ := DebugAddrs(spec.DebugSubnet)
		return ServiceAdapter, ad.String() + ":" + strconv.Itoa(a.Port)
	}
	return ServiceApp, inspectAddr(spec)
}

// composeAttach is one network of a service in the map form.
type composeAttach struct {
	IPv4Address string `yaml:"ipv4_address,omitempty"`
}

// debugApp puts the app on the debug network at its fixed address, mounts
// the adapter's tools read-only, adds its environment and AIMAN_INSPECT, and
// replaces app.run.
func debugApp(s *composeService, spec RunSpec) {
	app, _, _ := DebugAddrs(spec.DebugSubnet)
	d := spec.Debug
	s.Networks = map[string]composeAttach{
		NetworkInner: {},
		NetworkDebug: {IPv4Address: app.String()},
	}
	for _, m := range d.Mounts {
		s.Volumes = append(s.Volumes, m.Host+":"+m.Container+":ro")
	}
	if s.Environment == nil {
		s.Environment = map[string]string{}
	}
	maps.Copy(s.Environment, d.Env)
	s.Environment[InspectEnv] = inspectAddr(spec)
	if d.InspectPath != "" {
		s.Environment[InspectEnv] += "/" + d.InspectPath
	}
	s.Command = append([]string(nil), d.Command...)
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

// adapterService is the adapter container: aiman's pinned glibc image with
// the tools, src and out read-only, on the debug network alone, under the
// app's hardening and small limits. It reaches the app's inspector and
// nothing else; the edge reaches its DAP port.
func adapterService(spec RunSpec) composeService {
	_, ad, _ := DebugAddrs(spec.DebugSubnet)
	a := spec.Debug.Adapter
	vols := make([]string, 0, len(a.Mounts)+2)
	for _, m := range a.Mounts {
		vols = append(vols, m.Host+":"+m.Container+":ro")
	}
	vols = append(vols, spec.Src+":/src:ro", spec.Out+":/out:ro")
	s := composeService{
		Image:       spec.AdapterImage,
		PullPolicy:  pullNever,
		WorkingDir:  "/src",
		User:        fmt.Sprintf("%d:%d", spec.UID, spec.GID),
		ReadOnly:    true,
		CapDrop:     []string{capDropAll},
		SecurityOpt: []string{noNewPrivileges},
		Tmpfs:       []string{"/tmp"},
		Environment: map[string]string{"HOME": "/tmp", AdapterHostEnv: ad.String()},
		Volumes:     vols,
		Networks:    map[string]composeAttach{NetworkDebug: {IPv4Address: ad.String()}},
		Sysctls:     disableIPv6,
		Command:     append([]string(nil), a.Command...),
	}
	limited(&s, AdapterLimits)
	return s
}
