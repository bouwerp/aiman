package coordinator

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/config"
)

// ErrNoPreferredAgent means no agent in agents.preferred is installed on the
// host. The returned error wraps it and lists the commands tried, in order.
var ErrNoPreferredAgent = errors.New("no preferred agent installed")

// PickAgent returns the first entry in preferred whose agent is in installed,
// with its Agent trimmed. An agent may appear in several entries with
// different models; order is the preference. Commands are compared
// case-sensitively, as the host's agent binaries are.
func PickAgent(preferred []config.PreferredAgent, installed []string) (config.PreferredAgent, error) {
	var tried []string
	for _, p := range preferred {
		name := p.Command()
		if name == "" {
			continue
		}
		if slices.Contains(installed, name) {
			p.Agent = name
			return p, nil
		}
		if !slices.Contains(tried, name) {
			tried = append(tried, name)
		}
	}
	if len(tried) == 0 {
		return config.PreferredAgent{}, fmt.Errorf("%w: agents.preferred is empty", ErrNoPreferredAgent)
	}
	return config.PreferredAgent{}, fmt.Errorf("%w: tried %s", ErrNoPreferredAgent, strings.Join(tried, ", "))
}
