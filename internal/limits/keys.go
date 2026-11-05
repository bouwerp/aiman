package limits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	httpTimeout    = 10 * time.Second
	commandTimeout = 20 * time.Second
	sourceMinAge   = time.Minute

	maxResponseBytes = 1 << 20
	maxKeyFileBytes  = 1 << 20

	deepSeekHost = "api.deepseek.com"
)

// piCommandPrefix marks a Pi auth.json key that is a shell command, not a key.
// Sending it would put a command line in a provider's logs, and serve does not
// run commands from a settings file.
const piCommandPrefix = "!"

// source is the shared shape of the sources in this file's group.
type source struct {
	name    string
	timeout time.Duration
	read    func(ctx context.Context) Reading
}

func (s source) Name() string           { return s.name }
func (s source) MinAge() time.Duration  { return sourceMinAge }
func (s source) Timeout() time.Duration { return s.timeout }

func (s source) Read(ctx context.Context) Reading {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.read(ctx)
}

// unknownReading is a source that ran and could not read the answer. reason must
// never hold a key, a header, a URL or a response body.
func unknownReading(provider, reason string) Reading {
	return Reading{Provider: provider, State: StateUnknown, Reason: reason}
}

// unavailableReading has no reason because the view drops a source that simply
// does not apply on this host.
func unavailableReading(provider string) Reading {
	return Reading{Provider: provider, State: StateUnavailable}
}

// newTransport is the transport for provider calls. Proxy is nil because Go's
// default would send the Authorization header through HTTP_PROXY. Calls are a
// minute apart at least, so no connection is kept for the next one.
func newTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   httpTimeout,
		ResponseHeaderTimeout: httpTimeout,
		DisableKeepAlives:     true,
	}
}

func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// fetchJSON sends one authorised GET and decodes the answer into out. The
// returned reason is empty on success and carries at most a status code: a
// transport error is dropped because it quotes the URL.
func fetchJSON(ctx context.Context, env Env, target, key string, out any) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return "request failed"
	}
	req.Header.Set("Authorization", "Bearer "+key)

	var transport http.RoundTripper = newTransport()
	if env.Transport != nil {
		transport = env.Transport
	}
	client := &http.Client{Transport: transport, CheckRedirect: refuseRedirect}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "timed out"
		}
		return "request failed"
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "request failed"
	}
	if json.Unmarshal(body, out) != nil {
		return "unexpected response"
	}
	return ""
}

// commandFailure reads the outcome of Env.Run. ok is false when the command ran
// and exited 0.
func commandFailure(ctx context.Context, provider, name string, exit int, err error) (Reading, bool) {
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return unavailableReading(provider), true
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return unknownReading(provider, "timed out"), true
	case exit != 0:
		return unknownReading(provider, fmt.Sprintf("%s exited %d", name, exit)), true
	case err != nil:
		return unknownReading(provider, name+" failed"), true
	}
	return Reading{}, false
}

// readKeyFile decodes a settings file into out. A missing or malformed file is
// false: the caller falls back to the environment, and the file's text never
// reaches an error.
func readKeyFile(path string, out any) bool {
	f, err := os.Open(path) //nolint:gosec // G304: both paths are fixed locations under the user's home
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxKeyFileBytes))
	if err != nil {
		return false
	}
	return json.Unmarshal(raw, out) == nil
}

// openRouterKey reads .openrouter.key from Pi's auth file only for an api_key
// entry holding a literal key, else OPENROUTER_API_KEY. No other entry in that
// file is ever decoded, so another vendor's key cannot be picked.
func openRouterKey(env Env) string {
	var auth struct {
		OpenRouter struct {
			Type string `json:"type"`
			Key  string `json:"key"`
		} `json:"openrouter"`
	}
	if readKeyFile(filepath.Join(env.Home, ".pi", "agent", "auth.json"), &auth) && auth.OpenRouter.Type == "api_key" {
		key := strings.TrimSpace(auth.OpenRouter.Key)
		if key != "" && !strings.HasPrefix(key, piCommandPrefix) {
			return key
		}
	}
	return strings.TrimSpace(env.Getenv("OPENROUTER_API_KEY"))
}

// deepSeekKey reads .env.API_KEY from Deep Code's settings only when .env.BASE_URL
// is https on exactly api.deepseek.com, else DEEPSEEK_API_KEY. Deep Code can point
// at any provider, and then that key belongs to someone else.
func deepSeekKey(env Env) string {
	var settings struct {
		Env struct {
			APIKey  string `json:"API_KEY"`
			BaseURL string `json:"BASE_URL"`
		} `json:"env"`
	}
	if readKeyFile(filepath.Join(env.Home, ".deepcode", "settings.json"), &settings) && isDeepSeekBase(settings.Env.BaseURL) {
		if key := strings.TrimSpace(settings.Env.APIKey); key != "" {
			return key
		}
	}
	return strings.TrimSpace(env.Getenv("DEEPSEEK_API_KEY"))
}

func isDeepSeekBase(base string) bool {
	u, err := url.Parse(base)
	return err == nil && u.Scheme == "https" && u.Host == deepSeekHost
}
