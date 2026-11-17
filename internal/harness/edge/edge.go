// Package edge is the harness edge: the one container of a run that sits on
// both the inner network (app and services) and the outer bridge network the
// host publishes. It relays TCP from listeners on its outer address to
// declared inner host:port targets, and takes its relay map from serve over a
// token-checked Unix control socket. It never routes packets; compose turns
// ip_forward off on the container.
package edge

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"
)

// MinTokenBytes is the shortest control token the edge accepts.
const MinTokenBytes = 32

// Config is what the edge needs to start.
type Config struct {
	// Outer is the edge's address on the outer network. Every relay binds it
	// and nothing else, so no container on the inner network can reach one.
	Outer netip.Addr
	// Inner lists the inner network prefixes. An outer address inside one is
	// refused.
	Inner []netip.Prefix
	// Token is the per-run control token.
	Token []byte
	// LocalAddrs lists the addresses assigned to this host's interfaces. Nil
	// reads them from the system.
	LocalAddrs func() ([]netip.Addr, error)
	// DialTimeout bounds each dial to a target. Zero means 5s.
	DialTimeout time.Duration
}

// Edge owns the relays and answers the control socket.
type Edge struct {
	outer       netip.Addr
	token       []byte
	dialTimeout time.Duration
	started     time.Time

	mu     sync.Mutex
	relays map[string]*relay
	closed bool
}

// New checks the configuration and returns an edge with no relays.
func New(cfg Config) (*Edge, error) {
	if len(cfg.Token) < MinTokenBytes {
		return nil, fmt.Errorf("control token must be at least %d bytes", MinTokenBytes)
	}
	local := cfg.LocalAddrs
	if local == nil {
		local = SystemAddrs
	}
	if err := checkOuter(cfg.Outer, cfg.Inner, local); err != nil {
		return nil, err
	}
	timeout := cfg.DialTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Edge{
		outer:       cfg.Outer,
		token:       append([]byte(nil), cfg.Token...),
		dialTimeout: timeout,
		started:     time.Now(),
		relays:      map[string]*relay{},
	}, nil
}

// checkOuter refuses an outer address a relay must never bind: unset,
// wildcard, multicast, inside an inner prefix, or not assigned to this host.
func checkOuter(outer netip.Addr, inner []netip.Prefix, local func() ([]netip.Addr, error)) error {
	if !outer.IsValid() {
		return errors.New("outer address is not set")
	}
	outer = outer.Unmap()
	if outer.IsUnspecified() {
		return fmt.Errorf("outer address %s is a wildcard; relays bind the outer address only", outer)
	}
	if outer.IsMulticast() {
		return fmt.Errorf("outer address %s is multicast", outer)
	}
	for _, p := range inner {
		if p.Contains(outer) {
			return fmt.Errorf("outer address %s is on the inner network %s", outer, p)
		}
	}
	addrs, err := local()
	if err != nil {
		return fmt.Errorf("list local addresses: %w", err)
	}
	for _, a := range addrs {
		if a.Unmap() == outer {
			return nil
		}
	}
	return fmt.Errorf("outer address %s is not assigned to this host", outer)
}

// ResolveOuter turns the --outer value into an address: an address as is, or
// for a prefix the single local address inside it. Compose knows the outer
// network's subnet before Docker picks the edge's address in it.
func ResolveOuter(value string, local func() ([]netip.Addr, error)) (netip.Addr, error) {
	if a, err := netip.ParseAddr(value); err == nil {
		return a.Unmap(), nil
	}
	p, err := netip.ParsePrefix(value)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("outer %q is neither an address nor a prefix", value)
	}
	if local == nil {
		local = SystemAddrs
	}
	addrs, err := local()
	if err != nil {
		return netip.Addr{}, fmt.Errorf("list local addresses: %w", err)
	}
	var found []netip.Addr
	for _, a := range addrs {
		if p.Contains(a.Unmap()) {
			found = append(found, a.Unmap())
		}
	}
	if len(found) != 1 {
		return netip.Addr{}, fmt.Errorf("outer prefix %s matches %d local addresses, want 1", p, len(found))
	}
	return found[0], nil
}

// SystemAddrs lists the addresses assigned to this host's interfaces.
func SystemAddrs() ([]netip.Addr, error) {
	ifaddrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(ifaddrs))
	for _, ia := range ifaddrs {
		ipn, ok := ia.(*net.IPNet)
		if !ok {
			continue
		}
		if a, ok := netip.AddrFromSlice(ipn.IP); ok {
			out = append(out, a.Unmap())
		}
	}
	return out, nil
}

// RelaySpec declares one relay: a listener on the outer address at Port (0
// picks a free port) that forwards to Target, an inner host:port.
type RelaySpec struct {
	Name   string `json:"name"`
	Port   int    `json:"port"`
	Target string `json:"target"`
	// Kind is empty for plain TCP, or KindDAP for one debugger client at a
	// time.
	Kind string `json:"kind,omitempty"`
}

// RelayStatus is a relay with its bound address and counters.
type RelayStatus struct {
	RelaySpec
	Listen string `json:"listen"`
	Active int64  `json:"active"`
	Total  int64  `json:"total"`
	Failed int64  `json:"failed"`
	// Refused counts DAP clients turned away while another held the relay.
	Refused   int64  `json:"refused,omitempty"`
	LastError string `json:"last_error,omitempty"`
	// Holder is the DAP client that holds a KindDAP relay, if any.
	Holder *DAPHolder `json:"holder,omitempty"`
}

// Health is the edge's state for serve.
type Health struct {
	Outer   string        `json:"outer"`
	Started time.Time     `json:"started"`
	Relays  []RelayStatus `json:"relays"`
}

var relayName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)

func (e *Edge) checkSpec(spec RelaySpec) error {
	if !relayName.MatchString(spec.Name) {
		return errorf(CodeInvalidParams, "relay name %q must be 1-32 letters, digits, - or _", spec.Name)
	}
	if spec.Kind != "" && spec.Kind != KindDAP {
		return errorf(CodeInvalidParams, "relay kind %q must be empty or %s", spec.Kind, KindDAP)
	}
	if spec.Port < 0 || spec.Port > 65535 {
		return errorf(CodeInvalidParams, "relay port %d is out of range", spec.Port)
	}
	if spec.Kind == KindDAP && spec.Target == "" {
		// A run that debugs only its checks has no target until one starts.
		return nil
	}
	return e.checkTarget(spec.Target)
}

// checkTarget refuses a target that is not host:port, or is the edge's own
// outer address.
func (e *Edge) checkTarget(target string) error {
	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		return errorf(CodeInvalidParams, "relay target %q must be host:port", target)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return errorf(CodeInvalidParams, "relay target port %q is out of range", port)
	}
	if a, err := netip.ParseAddr(host); err == nil && a.Unmap() == e.outer {
		return errorf(CodeInvalidParams, "relay target %s is the edge's own outer address", target)
	}
	return nil
}

// Add starts a relay.
func (e *Edge) Add(spec RelaySpec) (RelayStatus, error) {
	if err := e.checkSpec(spec); err != nil {
		return RelayStatus{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return RelayStatus{}, errorf(CodeUnavailable, "edge is closing")
	}
	if _, ok := e.relays[spec.Name]; ok {
		return RelayStatus{}, errorf(CodeExists, "relay %q exists", spec.Name)
	}
	listen := net.JoinHostPort(e.outer.String(), strconv.Itoa(spec.Port))
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return RelayStatus{}, errorf(CodeBindFailed, "listen %s: %v", listen, err)
	}
	r := newRelay(spec, ln, e.dialTimeout)
	e.relays[spec.Name] = r
	go r.serve()
	return r.status(), nil
}

// Remove stops a relay and the connections it carries.
func (e *Edge) Remove(name string) error {
	e.mu.Lock()
	r, ok := e.relays[name]
	delete(e.relays, name)
	e.mu.Unlock()
	if !ok {
		return errorf(CodeNotFound, "relay %q not found", name)
	}
	r.close()
	return nil
}

// DropDAP ends the session of the client holding the DAP relay name, so the
// next client can take it, and returns who held it (nil for nobody).
func (e *Edge) DropDAP(name string) (*DAPHolder, error) {
	e.mu.Lock()
	r, ok := e.relays[name]
	e.mu.Unlock()
	if !ok {
		return nil, errorf(CodeNotFound, "relay %q not found", name)
	}
	if r.spec.Kind != KindDAP {
		return nil, errorf(CodeInvalidParams, "relay %q is not a debugger relay", name)
	}
	return r.drop(), nil
}

// SetDAPTarget points the DAP relay name at target (host:port, or empty for
// none), ending the current holder's lease and connections first, and
// returns who held it. Serve moves the relay to a debug check's debugger and
// back to the app's.
func (e *Edge) SetDAPTarget(name, target string) (*DAPHolder, error) {
	if target != "" {
		if err := e.checkTarget(target); err != nil {
			return nil, err
		}
	}
	e.mu.Lock()
	r, ok := e.relays[name]
	e.mu.Unlock()
	if !ok {
		return nil, errorf(CodeNotFound, "relay %q not found", name)
	}
	if r.spec.Kind != KindDAP {
		return nil, errorf(CodeInvalidParams, "relay %q is not a debugger relay", name)
	}
	return r.retarget(&target), nil
}

// List returns every relay, sorted by name.
func (e *Edge) List() []RelayStatus {
	e.mu.Lock()
	out := make([]RelayStatus, 0, len(e.relays))
	for _, r := range e.relays {
		out = append(out, r.status())
	}
	e.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Health reports the outer address, start time and relays.
func (e *Edge) Health() Health {
	return Health{Outer: e.outer.String(), Started: e.started, Relays: e.List()}
}

// Close stops every relay. Add fails afterwards.
func (e *Edge) Close() error {
	e.mu.Lock()
	e.closed = true
	relays := e.relays
	e.relays = map[string]*relay{}
	e.mu.Unlock()
	for _, r := range relays {
		r.close()
	}
	return nil
}
