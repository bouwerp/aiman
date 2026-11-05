package limits

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	orKey    = "sk-or-TESTKEYVALUE"
	otherKey = "sk-other-MUSTNOTLEAVE"
	dsKey    = "sk-ds-TESTKEYVALUE"
)

type recordTransport struct {
	reqs []*http.Request
	fn   func(*http.Request) (int, http.Header, string)
}

func (r *recordTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.reqs = append(r.reqs, req)
	code, h, body := r.fn(req)
	if h == nil {
		h = http.Header{}
	}
	return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(io.Discard) })
	return &buf
}

func piAuth(t *testing.T, env Env, kind string) {
	writeFile(t, filepath.Join(env.Home, ".pi/agent/auth.json"),
		`{"anthropic":{"type":"api_key","key":"`+otherKey+`"},"openrouter":{"type":"`+kind+`","key":"`+orKey+`"}}`)
}

func TestOpenRouterUsesOnlyItsPinnedKeyAndHost(t *testing.T) {
	env := testEnv(t)
	piAuth(t, env, "api_key")
	rt := &recordTransport{fn: func(*http.Request) (int, http.Header, string) {
		return 200, nil, `{"data":{"limit":100,"limit_remaining":74.5,"usage":25.5,"usage_daily":1.2,"usage_weekly":8}}`
	}}
	env.Transport = rt
	logs := captureLog(t)
	r := NewOpenRouterSource(env).Read(context.Background())
	if len(rt.reqs) != 1 {
		t.Fatalf("requests %d", len(rt.reqs))
	}
	got := rt.reqs[0]
	if got.URL.String() != "https://openrouter.ai/api/v1/key" || got.Header.Get("Authorization") != "Bearer "+orKey {
		t.Fatalf("request %s auth ok=%v", got.URL, got.Header.Get("Authorization") == "Bearer "+orKey)
	}
	if strings.Contains(got.URL.RawQuery, "sk-") {
		t.Fatal("key in the query string")
	}
	if r.State != StateOK || len(r.Balances) != 1 || !strings.HasPrefix(r.Balances[0].Amount, "74.5") {
		t.Fatalf("reading %+v", r)
	}
	if !strings.Contains(r.Spend, "1.2") || !strings.Contains(r.Spend, "8") {
		t.Fatalf("spend %q", r.Spend)
	}
	if strings.Contains(logs.String(), "sk-") {
		t.Fatal("key in the log")
	}
}

func TestOpenRouterNullRemainingIsUnlimited(t *testing.T) {
	env := testEnv(t)
	piAuth(t, env, "api_key")
	env.Transport = &recordTransport{fn: func(*http.Request) (int, http.Header, string) {
		return 200, nil, `{"data":{"limit":null,"limit_remaining":null,"usage":1,"usage_daily":0,"usage_weekly":1}}`
	}}
	r := NewOpenRouterSource(env).Read(context.Background())
	if r.State != StateOK || len(r.Balances) != 1 || !strings.Contains(strings.ToLower(r.Balances[0].Amount), "unlimited") {
		t.Fatalf("reading %+v", r)
	}
}

func TestOpenRouterIgnoresAnOtherKindOfEntryAndFallsBackToEnv(t *testing.T) {
	env := testEnv(t)
	piAuth(t, env, "oauth")
	if r := NewOpenRouterSource(env).Read(context.Background()); r.State != StateUnavailable {
		t.Fatalf("no usable key: %+v", r)
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
		t.Fatal("environment fallback not used")
	}
}

func TestProviderCallsRefuseRedirects(t *testing.T) {
	env := testEnv(t)
	piAuth(t, env, "api_key")
	rt := &recordTransport{fn: func(*http.Request) (int, http.Header, string) {
		return 301, http.Header{"Location": []string{"https://evil.example/steal"}}, ""
	}}
	env.Transport = rt
	logs := captureLog(t)
	r := NewOpenRouterSource(env).Read(context.Background())
	if len(rt.reqs) != 1 {
		t.Fatalf("followed a redirect: %d requests", len(rt.reqs))
	}
	if r.State != StateUnknown || strings.Contains(r.Reason, "evil") || strings.Contains(r.Reason, "sk-") {
		t.Fatalf("reading %+v", r)
	}
	if strings.Contains(logs.String(), "sk-") || strings.Contains(logs.String(), "evil") {
		t.Fatal("log carries the key or the redirect target")
	}
}

func TestProviderErrorCarriesStatusOnly(t *testing.T) {
	env := testEnv(t)
	piAuth(t, env, "api_key")
	env.Transport = &recordTransport{fn: func(*http.Request) (int, http.Header, string) { return 401, nil, "secret body text" }}
	r := NewOpenRouterSource(env).Read(context.Background())
	if r.State != StateUnknown || !strings.Contains(r.Reason, "401") || strings.Contains(r.Reason, "secret body") {
		t.Fatalf("reading %+v", r)
	}
}

func TestTransportIgnoresProxy(t *testing.T) {
	if newTransport().Proxy != nil {
		t.Fatal("provider transport would send keys through HTTP_PROXY")
	}
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "openrouter.ai"}}
	if p := newTransport().Proxy; p != nil {
		if u, _ := p(req); u != nil {
			t.Fatal("proxy in use")
		}
	}
}

func deepCode(t *testing.T, env Env, base string) {
	writeFile(t, filepath.Join(env.Home, ".deepcode/settings.json"), `{"env":{"API_KEY":"`+dsKey+`","BASE_URL":"`+base+`"}}`)
}

func TestDeepSeekReadsBalance(t *testing.T) {
	env := testEnv(t)
	deepCode(t, env, "https://api.deepseek.com")
	rt := &recordTransport{fn: func(*http.Request) (int, http.Header, string) {
		return 200, nil, `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"12.10","granted_balance":"0.00","topped_up_balance":"12.10"}]}`
	}}
	env.Transport = rt
	r := NewDeepSeekSource(env).Read(context.Background())
	if len(rt.reqs) != 1 || rt.reqs[0].URL.String() != "https://api.deepseek.com/user/balance" {
		t.Fatalf("requests %v", rt.reqs)
	}
	if r.State != StateOK || len(r.Balances) != 1 || r.Balances[0].Amount != "12.10" || r.Balances[0].Currency != "USD" {
		t.Fatalf("reading %+v", r)
	}
}

func TestDeepSeekKeyNeverGoesToAnotherProvidersSettings(t *testing.T) {
	for _, base := range []string{"https://api.deepseek.com.evil.example", "https://api.moonshot.ai/v1", "http://api.deepseek.com", "not a url"} {
		env := testEnv(t)
		deepCode(t, env, base)
		if r := NewDeepSeekSource(env).Read(context.Background()); r.State != StateUnavailable {
			t.Fatalf("base %q: %+v", base, r)
		}
	}
}

func TestDeepSeekFallsBackToEnv(t *testing.T) {
	env := testEnv(t)
	env.Getenv = func(k string) string {
		if k == "DEEPSEEK_API_KEY" {
			return "sk-ds-FROMENV"
		}
		return ""
	}
	rt := &recordTransport{fn: func(*http.Request) (int, http.Header, string) { return 200, nil, `{"balance_infos":[]}` }}
	env.Transport = rt
	NewDeepSeekSource(env).Read(context.Background())
	if len(rt.reqs) != 1 || rt.reqs[0].Header.Get("Authorization") != "Bearer sk-ds-FROMENV" {
		t.Fatal("environment fallback not used")
	}
}

func TestCopilotReportsPremiumRequests(t *testing.T) {
	env := testEnv(t)
	body := `{"usageItems":[{"grossQuantity":10},{"grossQuantity":2.5}]}`
	env.Run = func(_ context.Context, name string, args ...string) (string, int, error) {
		if name != "gh" {
			t.Errorf("ran %s", name)
		}
		if strings.Join(args, " ") == "api user --jq .login" {
			return "someone\n", 0, nil
		}
		if strings.Contains(strings.Join(args, " "), "/users/someone/settings/billing/premium_request/usage") {
			return body, 0, nil
		}
		t.Errorf("args %v", args)
		return "", 1, nil
	}
	if r := NewCopilotSource(env).Read(context.Background()); r.State != StateOK || !strings.Contains(r.Spend, "12.5") {
		t.Fatalf("reading %+v", r)
	}
	body = `{"usageItems":[]}`
	if r := NewCopilotSource(env).Read(context.Background()); r.State != StateOK || r.Spend == "" {
		t.Fatalf("no usage: %+v", r)
	}
}

func TestCopilotWithoutGhIsUnavailable(t *testing.T) {
	env := testEnv(t)
	env.Run = func(context.Context, string, ...string) (string, int, error) { return "", 0, exec.ErrNotFound }
	if r := NewCopilotSource(env).Read(context.Background()); r.State != StateUnavailable {
		t.Fatalf("reading %+v", r)
	}
}

func TestOpenCodeReportsSpendOnly(t *testing.T) {
	env := testEnv(t)
	env.Run = func(_ context.Context, name string, args ...string) (string, int, error) {
		if name != "opencode" || strings.Join(args, " ") != "stats --days 7" {
			t.Errorf("ran %s %v", name, args)
		}
		return "│Total Cost                                      $159.50 │\n", 0, nil
	}
	r := NewOpenCodeSource(env).Read(context.Background())
	if r.State != StateOK || !strings.Contains(r.Spend, "$159.50") || len(r.Windows) != 0 {
		t.Fatalf("reading %+v", r)
	}
	env.Run = func(context.Context, string, ...string) (string, int, error) { return "changed screen", 0, nil }
	if r := NewOpenCodeSource(env).Read(context.Background()); r.State != StateUnknown {
		t.Fatalf("changed screen: %+v", r)
	}
	env.Run = func(context.Context, string, ...string) (string, int, error) { return "", 0, exec.ErrNotFound }
	if r := NewOpenCodeSource(env).Read(context.Background()); r.State != StateUnavailable {
		t.Fatalf("not installed: %+v", r)
	}
}
