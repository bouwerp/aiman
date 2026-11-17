package harness

import (
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
)

// Debug runs. The app joins a third network, debug, beside inner. Only the
// app and the edge are on it: a service cannot reach the debug port, so a
// hostile service image cannot attach and evaluate code in the app. Every
// container on it has a fixed address (DebugAddrs), because Node's inspector
// refuses a Host header that is neither an IP address nor localhost; the
// debug adapter listens on the app's debug address only, and the edge relays
// it under a lease.
const (
	NetworkDebug = "debug"
	// RelayDAP names the debugger's relay and its forwarded port.
	RelayDAP = "dap"
	// RelayKindDAP marks a relay that holds DAP clients under a lease.
	RelayKindDAP = "dap"
	// ToolsMount is where debug tools are mounted read-only in the app.
	ToolsMount = "/aiman-tools"
	// InspectEnv carries the app's debug address and the adapter's port,
	// <ip>:<port>, into the app container. A debug command listens on it.
	InspectEnv = "AIMAN_INSPECT"
)

// DebugAddrs are the fixed addresses on a debug subnet: .2 the app (the
// debug target), .3 a separate adapter container (reserved for adapters that
// do not run in the target), .4 the edge.
func DebugAddrs(subnet netip.Prefix) (app, adapter, edge netip.Addr) {
	base := subnet.Masked().Addr()
	app = base.Next().Next()
	adapter = app.Next()
	edge = adapter.Next()
	return app, adapter, edge
}

// DebugSpec is how one debug run starts its app. A DebugAdapter makes it;
// Compose only places it, so the compose builder knows no debugger.
type DebugSpec struct {
	// Lang is the file's app.debug.lang the adapter serves.
	Lang string
	// Command replaces app.run. It must keep the adapter listening on
	// $AIMAN_INSPECT, restarting it after each client leaves.
	Command []string
	// Env joins the app's environment for the build and the app.
	Env map[string]string
	// Mounts are host files the adapter needs, mounted read-only.
	Mounts []ToolMount
	// Port is the DAP port on the app's debug address.
	Port int
}

// ToolMount is one read-only bind mount of a host file.
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
	if len(d.Command) == 0 || d.Port < 1 || d.Port > 65535 {
		return errors.New("harness compose: the debug spec needs a command and a port")
	}
	for _, m := range d.Mounts {
		if !filepath.IsAbs(m.Host) || strings.ContainsAny(m.Host, ":,\n") {
			return fmt.Errorf("harness compose: debug tool path %q must be absolute with no colon or comma", m.Host)
		}
		if !strings.HasPrefix(m.Container, ToolsMount+"/") || strings.ContainsAny(m.Container, ":,\n") {
			return fmt.Errorf("harness compose: debug tool mount %q must lie under %s", m.Container, ToolsMount)
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

// debugEndpoint is the app's debug address and the adapter's port.
func debugEndpoint(spec RunSpec) string {
	app, _, _ := DebugAddrs(spec.DebugSubnet)
	return app.String() + ":" + strconv.Itoa(spec.Debug.Port)
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
	s.Environment[InspectEnv] = debugEndpoint(spec)
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
