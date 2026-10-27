package coordinator

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrNoPreferredAgent means no name in agents.preferred is installed on the
// host. The returned error wraps it and lists the names tried, in order.
var ErrNoPreferredAgent = errors.New("no preferred agent installed")

// PickAgent returns the first name in preferred that is in installed. Names
// are trimmed and compared case-sensitively, as the host's agent binaries are.
func PickAgent(preferred, installed []string) (string, error) {
	var tried []string
	for _, name := range preferred {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if slices.Contains(installed, name) {
			return name, nil
		}
		tried = append(tried, name)
	}
	if len(tried) == 0 {
		return "", fmt.Errorf("%w: agents.preferred is empty", ErrNoPreferredAgent)
	}
	return "", fmt.Errorf("%w: tried %s", ErrNoPreferredAgent, strings.Join(tried, ", "))
}
