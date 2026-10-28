package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ModelOption is one model an agent can launch with. ID is the exact value
// for the agent's model setting; for Pi it is "<provider>/<model>". Provider
// is empty for agents whose catalog has no provider split.
type ModelOption struct {
	Provider string
	ID       string
	Label    string
}

// ProviderCount is one provider in a model list and how many models it has.
type ProviderCount struct {
	Name  string
	Count int
}

// HasDynamicModels reports whether the agent's models are listed by running
// the agent on the host rather than read from LaunchCatalog. Only Pi does.
func HasDynamicModels(command string) bool {
	return CommandBase(command) == "pi"
}

// StaticModels lists the agent's LaunchCatalog models. Unknown agents have none.
func StaticModels(command string) []ModelOption {
	models := LaunchCatalogFor(CommandBase(command)).Models
	if len(models) == 0 {
		return nil
	}
	out := make([]ModelOption, 0, len(models))
	for _, m := range models {
		out = append(out, ModelOption{ID: m, Label: m})
	}
	return out
}

// piListModelsCmd lists every Pi model on the host. It adds the usual user
// bin directories so a non-login SSH shell still finds pi, and drops stderr
// so warnings do not reach the parser.
const piListModelsCmd = `bash -lc 'export PATH="$PATH:$HOME/.local/bin:$HOME/.npm-global/bin:$HOME/bin:$HOME/.bun/bin:/usr/local/bin:/opt/homebrew/bin"; pi --list-models 2>/dev/null'`

// ParsePiModels reads `pi --list-models` output: a header row
// (provider model context max-out thinking images) and one row per model.
// Rows that do not have at least provider and model are skipped.
func ParsePiModels(out string) []ModelOption {
	var opts []ModelOption
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] == "provider" || strings.HasSuffix(f[0], ":") {
			continue
		}
		label := f[1]
		if len(f) >= 3 {
			label += "  " + f[2] + " ctx"
		}
		if len(f) >= 5 && f[4] == "yes" {
			label += "  thinking"
		}
		opts = append(opts, ModelOption{Provider: f[0], ID: f[0] + "/" + f[1], Label: label})
	}
	return opts
}

// ModelProviders counts models per provider in first-seen order.
func ModelProviders(opts []ModelOption) []ProviderCount {
	var out []ProviderCount
	idx := map[string]int{}
	for _, o := range opts {
		i, ok := idx[o.Provider]
		if !ok {
			i = len(out)
			idx[o.Provider] = i
			out = append(out, ProviderCount{Name: o.Provider})
		}
		out[i].Count++
	}
	return out
}

// ModelCache lists agent models per host and keeps each successful dynamic
// listing for the life of the cache. Failures are not kept, so a retry runs
// the command again.
type ModelCache struct {
	mu    sync.Mutex
	lists map[string][]ModelOption
}

// NewModelCache returns an empty cache.
func NewModelCache() *ModelCache {
	return &ModelCache{lists: map[string][]ModelOption{}}
}

// List returns the models for command on host: the static catalog, or for
// Pi the host's `pi --list-models` output.
func (c *ModelCache) List(ctx context.Context, host string, exec Executor, command string) ([]ModelOption, error) {
	if !HasDynamicModels(command) {
		return StaticModels(command), nil
	}
	key := host + "\x00" + CommandBase(command)
	c.mu.Lock()
	cached, ok := c.lists[key]
	c.mu.Unlock()
	if ok {
		return cached, nil
	}
	if exec == nil {
		return nil, errors.New("no remote to list models on")
	}
	out, err := exec.Execute(ctx, piListModelsCmd)
	if err != nil {
		return nil, fmt.Errorf("pi --list-models on %s: %w", host, err)
	}
	opts := ParsePiModels(out)
	if len(opts) == 0 {
		return nil, fmt.Errorf("pi --list-models on %s listed no models", host)
	}
	c.mu.Lock()
	c.lists[key] = opts
	c.mu.Unlock()
	return opts, nil
}
