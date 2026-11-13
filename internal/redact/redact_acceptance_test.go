package redact_test

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/redact"
)

// Acceptance tests for daemon-control piece A (log line redaction). Do not edit.

func TestLineRedactsSecrets(t *testing.T) {
	cases := map[string]string{
		"slack bot":      "posting with xoxb-1234567890-0987654321-abcdefABCDEF now",
		"slack user":     "token xoxp-1111111111-2222222222-abcdef",
		"slack webhook":  "POST https://hooks.slack.com/services/T0000000/B0000000/abcdefghijklmnopqrstuvwx",
		"github classic": "gh auth ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"github fine":    "pat github_pat_11ABCDEFG0abcdefghijkl_mnopqrstuvwxyz0123456789ABCDEFGHIJ",
		"anthropic":      "key sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789",
		"openai":         "key sk-proj-abcdefghijklmnopqrstuvwxyz012345",
		"xai":            "key xai-abcdefghijklmnopqrstuvwxyz0123456789",
		"expo":           "push ExponentPushToken[xxxxxxxxxxxxxxxxxxxxxx] ok",
		"aws key id":     "using AKIAIOSFODNN7EXAMPLE",
		"aws secret":     "aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"bearer":         "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc",
		"url userinfo":   "dial postgres://admin:hunter2pass@db.internal:5432/x",
		"query token":    "GET /v1/events?access_token=abcdef0123456789abcdef",
		"password kv":    "config password=hunter2pass loaded",
		"pem":            "-----BEGIN PRIVATE KEY-----",
	}
	secrets := map[string]string{
		"slack bot": "xoxb-1234567890", "slack user": "xoxp-1111111111", "slack webhook": "abcdefghijklmnopqrstuvwx",
		"github classic": "ghp_abcdefghij", "github fine": "github_pat_11ABCDEFG0", "anthropic": "sk-ant-api03-abcdef",
		"openai": "sk-proj-abcdef", "xai": "xai-abcdefghij", "expo": "ExponentPushToken[xxxx", "aws key id": "AKIAIOSFODNN7EXAMPLE",
		"aws secret": "wJalrXUtnFEMI", "bearer": "eyJhbGciOiJIUzI1NiJ9", "url userinfo": "hunter2pass",
		"query token": "abcdef0123456789abcdef", "password kv": "hunter2pass", "pem": "BEGIN PRIVATE KEY",
	}
	for name, in := range cases {
		out, n := redact.Line(in, nil)
		if n == 0 || strings.Contains(out, secrets[name]) || !strings.Contains(out, "[redacted]") {
			t.Errorf("%s: Line(%q) = %q, %d", name, in, out, n)
		}
	}
}

func TestLineKeepsOrdinaryText(t *testing.T) {
	for _, in := range []string{
		"serve: listening on /home/code/.aiman/aiman.sock",
		"session fix-login (3f2a9c1e-0b7d-4e55-9a1f-2c3d4e5f6a7b) started agent claude",
		"host update v0.46.0 -> v0.47.0 ok",
		"GET /v1/rpc 200 12ms method=session.list",
	} {
		if out, n := redact.Line(in, nil); out != in || n != 0 {
			t.Errorf("Line(%q) = %q, %d; want unchanged", in, out, n)
		}
	}
}

func TestLineRedactsExactValues(t *testing.T) {
	const tok = "gw-7f3a91c2d4e5b6a7"
	out, n := redact.Line("auth with "+tok+" from phone", []string{tok, ""})
	if strings.Contains(out, tok) || n != 1 {
		t.Fatalf("Line = %q, %d", out, n)
	}
}
