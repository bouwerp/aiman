package selfupdate

import (
	"context"
	"errors"
	"time"

	"github.com/bouwerp/aiman/internal/infra/remotesvc"
)

// Gateway is what one read of the host's aiman-gateway finds.
type Gateway struct {
	Installed bool
	// Version is the gateway's --version output, ideally "vX.Y.Z".
	Version string
	Running bool
}

// Gateway status values in Status.Gateway.
const (
	GatewayCurrent      = "current"
	GatewayBehind       = "behind"
	GatewayUpdating     = "updating"
	GatewayFailed       = "failed"
	GatewayUnknown      = "unknown"
	GatewayNotInstalled = "not_installed"
)

// GatewayStatus is the gateway block of Status.
type GatewayStatus struct {
	Installed bool
	Version   string
	Running   bool
	Status    string
}

// ReasonGateway is Outcome.Reason for an accepted gateway repair.
const ReasonGateway = "gateway"

const (
	// gatewayReadTimeout bounds one ReadGateway call.
	gatewayReadTimeout = 5 * time.Second
	// gatewayCacheTTL is how long a read answers Status and Poll.
	gatewayCacheTTL = 5 * time.Second
)

var errGatewayReadRunning = errors.New("gateway read in progress")

// gatewayRead is one cached ReadGateway answer.
type gatewayRead struct {
	gw  Gateway
	err error
	at  time.Time
}

func (m *Manager) gatewayScript(tag string) string {
	if m.GatewayScript != nil {
		return m.GatewayScript(tag)
	}
	return remotesvc.GatewayRepairScript(tag)
}

// readGateway returns a read no older than gatewayCacheTTL, reading again when
// needed. It holds no operation lock. One read runs at a time: with wait false
// a caller arriving during a read gets the previous answer (or an unknown one)
// instead of waiting. It returns nil when ReadGateway is unset.
func (m *Manager) readGateway(ctx context.Context, wait bool) *gatewayRead {
	if m.ReadGateway == nil {
		return nil
	}
	for {
		m.gwMu.Lock()
		c := m.gwCache
		if c != nil && m.now().Sub(c.at) < gatewayCacheTTL {
			m.gwMu.Unlock()
			return c
		}
		if f := m.gwFlight; f != nil {
			m.gwMu.Unlock()
			if c == nil {
				c = &gatewayRead{err: errGatewayReadRunning}
			}
			if !wait {
				return c
			}
			select {
			case <-f:
				continue
			case <-ctx.Done():
				return c
			}
		}
		f := make(chan struct{})
		m.gwFlight = f
		m.gwMu.Unlock()

		rctx, cancel := context.WithTimeout(ctx, gatewayReadTimeout)
		gw, err := m.ReadGateway(rctx)
		cancel()
		r := &gatewayRead{gw: gw, err: err, at: m.now()}
		if err != nil {
			m.logf("host-update: read gateway: %v", err)
		}

		m.gwMu.Lock()
		m.gwCache, m.gwFlight = r, nil
		m.gwMu.Unlock()
		close(f)
		return r
	}
}

// gatewayFailedAt reports a failed attempt to bring the gateway to serve's
// tag: a failed gateway repair, or (with a reader) a serve install at this
// version whose lockstep failed.
func (m *Manager) gatewayFailedAt(a *Attempt) bool {
	return a != nil && a.Status == AttemptFailed && a.To == m.Version
}

// gatewayState classifies the gateway against serve's version.
func (m *Manager) gatewayState(s State, g *gatewayRead) string {
	a := s.Attempt
	switch {
	case a.IsGateway() && a.Status == AttemptStarted:
		return GatewayUpdating
	case g.err != nil:
		return GatewayUnknown
	case !g.gw.Installed:
		return GatewayNotInstalled
	}
	if _, ok := remotesvc.ParseVersion(g.gw.Version); !ok {
		return GatewayUnknown
	}
	if _, _, behind := remotesvc.Outdated(g.gw.Version, m.Version); !behind {
		return GatewayCurrent
	}
	if m.gatewayFailedAt(a) {
		return GatewayFailed
	}
	return GatewayBehind
}

func (m *Manager) gatewayStatus(s State, g *gatewayRead) *GatewayStatus {
	if g == nil {
		return nil
	}
	return &GatewayStatus{
		Installed: g.gw.Installed,
		Version:   g.gw.Version,
		Running:   g.gw.Running,
		Status:    m.gatewayState(s, g),
	}
}

// judgeGateway decides a gateway repair once serve is not behind a known
// release. v is serve's verdict; the result keeps its status, since the host
// status describes serve. forced (host.update) skips the retry interval.
func (m *Manager) judgeGateway(s State, g *gatewayRead, v verdict, forced bool) verdict {
	a := s.Attempt
	lockstepFailed := !a.IsGateway() && m.gatewayFailedAt(a)
	switch st := m.gatewayState(s, g); {
	case st == GatewayBehind, st == GatewayFailed:
	case lockstepFailed && st == GatewayUnknown:
	default:
		return v
	}
	if m.gatewayFailedAt(a) && !forced && m.now().Sub(a.At) < m.Policy.retryAfter() {
		return v
	}
	return verdict{status: v.status, apply: true, tag: m.Version, kind: AttemptKindGateway}
}

// gatewayKey is the notify-once key for one drift.
func gatewayKey(g *gatewayRead, tag string) string {
	return g.gw.Version + "@" + tag
}
