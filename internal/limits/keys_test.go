package limits

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
)

func TestKeysPiCommandIsNeverSentOrRun(t *testing.T) {
	for _, value := range []string{"!cat ~/.secret", "!", "   "} {
		env := testEnv(t)
		writeFile(t, filepath.Join(env.Home, ".pi/agent/auth.json"),
			`{"openrouter":{"type":"api_key","key":"`+value+`"}}`)
		if r := NewOpenRouterSource(env).Read(context.Background()); r.State != StateUnavailable {
			t.Fatalf("key %q: %+v", value, r)
		}
		env.Getenv = func(k string) string {
			if k == "OPENROUTER_API_KEY" {
				return "sk-or-FROMENV"
			}
			return ""
		}
		rt := &recordTransport{fn: func(*http.Request) (int, http.Header, string) { return 200, nil, `{"data":{"limit_remaining":1}}` }}
		env.Transport = rt
		NewOpenRouterSource(env).Read(context.Background())
		if len(rt.reqs) != 1 || rt.reqs[0].Header.Get("Authorization") != "Bearer sk-or-FROMENV" {
			t.Fatalf("key %q: environment fallback not used", value)
		}
	}
}

func TestKeysMalformedFileFallsBackToEnvWithoutQuotingIt(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Home, ".pi/agent/auth.json"), `{"openrouter":{"key":"sk-or-BROKEN`)
	r := NewOpenRouterSource(env).Read(context.Background())
	if r.State != StateUnavailable || r.Reason != "" {
		t.Fatalf("reading %+v", r)
	}
}

func TestOpenRouterMissingRemainingIsNotUnlimited(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":{"usage":1}}`, `{"data":{"limit_remaining":"lots"}}`, `not json`} {
		env := testEnv(t)
		piAuth(t, env, "api_key")
		env.Transport = &recordTransport{fn: func(*http.Request) (int, http.Header, string) { return 200, nil, body }}
		if r := NewOpenRouterSource(env).Read(context.Background()); r.State != StateUnknown || len(r.Balances) != 0 {
			t.Fatalf("body %q: %+v", body, r)
		}
	}
}

func TestProviderTransportFailureDoesNotQuoteTheURL(t *testing.T) {
	env := testEnv(t)
	piAuth(t, env, "api_key")
	env.Transport = failTransport{}
	r := NewOpenRouterSource(env).Read(context.Background())
	if r.State != StateUnknown || r.Reason != "request failed" {
		t.Fatalf("reading %+v", r)
	}
}

type failTransport struct{}

func (failTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial failed")
}

func TestDeepSeekRefusesAPartialAnswer(t *testing.T) {
	for _, body := range []string{
		`{"balance_infos":[]}`,
		`{"balance_infos":[{"currency":"USD","total_balance":"1.00"},{"currency":"CNY"}]}`,
		`{"balance_infos":[{"currency":"","total_balance":"1.00"}]}`,
	} {
		env := testEnv(t)
		deepCode(t, env, "https://api.deepseek.com/v1")
		env.Transport = &recordTransport{fn: func(*http.Request) (int, http.Header, string) { return 200, nil, body }}
		if r := NewDeepSeekSource(env).Read(context.Background()); r.State != StateUnknown || len(r.Balances) != 0 {
			t.Fatalf("body %q: %+v", body, r)
		}
	}
}

func TestCopilotWithoutLoginIsUnavailableAndOtherFailuresAreUnknown(t *testing.T) {
	env := testEnv(t)
	env.Run = func(context.Context, string, ...string) (string, int, error) { return "", ghExitAuth, nil }
	if r := NewCopilotSource(env).Read(context.Background()); r.State != StateUnavailable {
		t.Fatalf("no login: %+v", r)
	}
	env.Run = func(_ context.Context, _ string, args ...string) (string, int, error) {
		if args[1] == "user" {
			return "someone\n", 0, nil
		}
		return "", 1, nil
	}
	if r := NewCopilotSource(env).Read(context.Background()); r.State != StateUnknown || r.Reason != "gh exited 1" {
		t.Fatalf("api failure: %+v", r)
	}
	for _, out := range []string{`{}`, `{"usageItems":[{}]}`, `not json`} {
		env.Run = func(_ context.Context, _ string, args ...string) (string, int, error) {
			if args[1] == "user" {
				return "someone\n", 0, nil
			}
			return out, 0, nil
		}
		if r := NewCopilotSource(env).Read(context.Background()); r.State != StateUnknown {
			t.Fatalf("output %q: %+v", out, r)
		}
	}
	env.Run = func(context.Context, string, ...string) (string, int, error) { return "../../evil\n", 0, nil }
	if r := NewCopilotSource(env).Read(context.Background()); r.State != StateUnknown {
		t.Fatalf("odd login: %+v", r)
	}
}

func TestProviderSourcesReportTheirTimeLimits(t *testing.T) {
	env := testEnv(t)
	for _, tc := range []struct {
		src  Source
		name string
		want string
	}{
		{NewOpenRouterSource(env), "openrouter", "10s"},
		{NewDeepSeekSource(env), "deepseek", "10s"},
		{NewCopilotSource(env), "copilot", "20s"},
		{NewOpenCodeSource(env), "opencode", "20s"},
	} {
		if tc.src.Name() != tc.name || tc.src.Timeout().String() != tc.want || tc.src.MinAge() != sourceMinAge {
			t.Errorf("%s: name %s timeout %s min age %s", tc.name, tc.src.Name(), tc.src.Timeout(), tc.src.MinAge())
		}
	}
}
