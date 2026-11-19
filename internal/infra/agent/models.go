package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
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
// the agent on the host (modelListers) rather than read from LaunchCatalog.
func HasDynamicModels(command string) bool {
	_, ok := modelListers[CommandBase(command)]
	return ok
}

// FallbackModels is what a picker shows when the host's listing is not
// available: the static catalog, except for Pi, whose models depend on the
// providers set up on that host, so a fixed list would mislead.
func FallbackModels(command string) []ModelOption {
	if CommandBase(command) == "pi" {
		return nil
	}
	return StaticModels(command)
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
// listing for TTL (zero is the life of the cache). Failures are not kept, so
// a retry runs the command again.
type ModelCache struct {
	// TTL is how long a listing stands; zero keeps it until Reset.
	TTL time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu    sync.Mutex
	lists map[string]cachedModels
}

type cachedModels struct {
	opts []ModelOption
	at   time.Time
}

// NewModelCache returns an empty cache.
func NewModelCache() *ModelCache {
	return &ModelCache{lists: map[string]cachedModels{}}
}

func (c *ModelCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Reset drops every listing, so the next List runs the agents again (after
// an agent update, or a status call that asks for a refresh).
func (c *ModelCache) Reset() {
	c.mu.Lock()
	c.lists = map[string]cachedModels{}
	c.mu.Unlock()
}

// List returns the models for command on host: what the agent's own listing
// command prints there (modelListers), else the static catalog. A failed or
// empty listing is an error, so the caller can say why and offer typing.
func (c *ModelCache) List(ctx context.Context, host string, exec Executor, command string) ([]ModelOption, error) {
	base := CommandBase(command)
	lister, ok := modelListers[base]
	if !ok {
		return StaticModels(command), nil
	}
	key := host + "\x00" + base
	c.mu.Lock()
	cached, ok := c.lists[key]
	c.mu.Unlock()
	if ok && (c.TTL <= 0 || c.now().Sub(cached.at) < c.TTL) {
		return cached.opts, nil
	}
	if exec == nil {
		return nil, errors.New("no remote to list models on")
	}
	bin := lister.bin
	if bin == "" {
		bin = base
	}
	out, err := exec.Execute(ctx, listModelsCmd(bin, lister.args))
	if err != nil {
		return nil, fmt.Errorf("%s %s on %s: %w", bin, lister.args, host, err)
	}
	opts := lister.parse(out)
	if len(opts) == 0 {
		return nil, fmt.Errorf("%s %s on %s listed no models", bin, lister.args, host)
	}
	c.mu.Lock()
	c.lists[key] = cachedModels{opts: opts, at: c.now()}
	c.mu.Unlock()
	return opts, nil
}
