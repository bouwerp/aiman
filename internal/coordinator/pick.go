package coordinator

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/config"
)

// ErrNoPreferredAgent means no agent in the configured agent lists is
// installed on the host. The returned error wraps it and lists the commands
// tried, in order.
var ErrNoPreferredAgent = errors.New("no preferred agent installed")

// PickAgent returns the first entry in preferred whose agent is in installed,
// with its Agent trimmed. An agent may appear in several entries with
// different models; order is the preference. Commands are compared
// case-sensitively, as the host's agent binaries are.
func PickAgent(preferred []config.PreferredAgent, installed []string) (config.PreferredAgent, error) {
	p, tried, ok := pickFrom(preferred, installed)
	if ok {
		return p, nil
	}
	if len(tried) == 0 {
		return config.PreferredAgent{}, fmt.Errorf("%w: agents.preferred is empty", ErrNoPreferredAgent)
	}
	return config.PreferredAgent{}, fmt.Errorf("%w: tried %s", ErrNoPreferredAgent, strings.Join(tried, ", "))
}

// pickFrom is PickAgent without the error: the picked entry, or the distinct
// commands it tried in order.
func pickFrom(list []config.PreferredAgent, installed []string) (config.PreferredAgent, []string, bool) {
	var tried []string
	for _, p := range list {
		name := p.Command()
		if name == "" {
			continue
		}
		if slices.Contains(installed, name) {
			p.Agent = name
			return p, nil, true
		}
		if !slices.Contains(tried, name) {
			tried = append(tried, name)
		}
	}
	return config.PreferredAgent{}, tried, false
}

// Pick is the outcome of PickTier. Tier is the tier Entry came from and
// Requested the tier asked for; FellBack is true when they differ.
type Pick struct {
	Entry     config.PreferredAgent
	Tier      string
	Requested string
	FellBack  bool
}

// PickTier returns the first installed entry of the wanted tier. Empty want is
// the worker tier. When the wanted tier is empty or has nothing installed, it
// picks from the other tier and sets FellBack; this holds both ways, so a
// worker request may land on a smart entry. When neither tier has an
// installed agent the error wraps ErrNoPreferredAgent and lists what each
// tier tried. An unknown tier name is an error that does not wrap it.
func PickTier(a config.AgentsConfig, want string, installed []string) (Pick, error) {
	if want == "" {
		want = config.TierWorker
	}
	if !config.ValidTier(want) {
		return Pick{}, fmt.Errorf("unknown tier %q (want %s or %s)", want, config.TierSmart, config.TierWorker)
	}
	other := config.TierSmart
	if want == config.TierSmart {
		other = config.TierWorker
	}
	var missing []string
	for _, tier := range []string{want, other} {
		e, tried, ok := pickFrom(a.Tier(tier), installed)
		if ok {
			return Pick{Entry: e, Tier: tier, Requested: want, FellBack: tier != want}, nil
		}
		if len(tried) == 0 {
			missing = append(missing, tier+": empty")
		} else {
			missing = append(missing, tier+": tried "+strings.Join(tried, ", "))
		}
	}
	return Pick{}, fmt.Errorf("%w: %s", ErrNoPreferredAgent, strings.Join(missing, "; "))
}
