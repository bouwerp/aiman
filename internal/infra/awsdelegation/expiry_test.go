package awsdelegation

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestFormatCredentialsSectionWritesExpiry(t *testing.T) {
	exp := time.Date(2026, 7, 29, 18, 30, 0, 0, time.UTC)
	got := FormatCredentialsSection("prod", &SessionCredentials{
		AccessKeyID:     "AKIA",
		SecretAccessKey: "secret",
		SessionToken:    "token",
		Expiration:      exp,
	})
	if !strings.Contains(got, ExpiryKey+" = 2026-07-29T18:30:00Z") {
		t.Fatalf("expected the expiry line in the section, got:\n%s", got)
	}
}

func TestFormatCredentialsSectionOmitsZeroExpiry(t *testing.T) {
	got := FormatCredentialsSection("prod", &SessionCredentials{AccessKeyID: "AKIA"})
	if strings.Contains(got, ExpiryKey) {
		t.Fatalf("must not write an empty expiry line, got:\n%s", got)
	}
}

func TestParseCredentialExpirations(t *testing.T) {
	content := `# aiman: delegated profile below
[default]
aws_access_key_id = AKIA1
aws_secret_access_key = s1
aws_session_token = t1
x_security_token_expires = 2026-07-29T18:30:00Z

[prod]
aws_access_key_id = AKIA2
x_security_token_expires = 2026-07-29T20:00:00+02:00

[legacy]
aws_access_key_id = AKIA3

[broken]
x_security_token_expires = not-a-timestamp
`
	got := ParseCredentialExpirations(content)
	if len(got) != 2 {
		t.Fatalf("expected 2 parsed expirations, got %d: %v", len(got), got)
	}
	if want := time.Date(2026, 7, 29, 18, 30, 0, 0, time.UTC); !got["default"].Equal(want) {
		t.Fatalf("default: got %v, want %v", got["default"], want)
	}
	if want := time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC); !got["prod"].Equal(want) {
		t.Fatalf("prod: got %v, want %v", got["prod"], want)
	}
	if _, ok := got["legacy"]; ok {
		t.Fatal("a section without an expiry key must be absent from the map")
	}
	if _, ok := got["broken"]; ok {
		t.Fatal("an unparseable timestamp must be absent from the map")
	}
}

func TestParseCredentialExpirationsEmpty(t *testing.T) {
	if got := ParseCredentialExpirations(""); len(got) != 0 {
		t.Fatalf("expected no expirations, got %v", got)
	}
}

// fakeRunner serves canned output for a single Execute call.
type fakeRunner struct {
	out     string
	err     error
	lastCmd string
}

func (f *fakeRunner) Execute(_ context.Context, cmd string) (string, error) {
	f.lastCmd = cmd
	return f.out, f.err
}

func (f *fakeRunner) WriteFile(_ context.Context, _ string, _ []byte) error { return nil }

func TestReadCredentialExpirations(t *testing.T) {
	r := &fakeRunner{out: `1785000000
` + credentialsPayloadMarker + `
[prod]
x_security_token_expires = 2026-07-29T18:30:00Z
`}
	got, err := ReadCredentialExpirations(context.Background(), r)
	if err != nil {
		t.Fatalf("ReadCredentialExpirations: %v", err)
	}
	if want := time.Date(2026, 7, 29, 18, 30, 0, 0, time.UTC); !got.Expirations["prod"].Equal(want) {
		t.Fatalf("prod expiry: got %v, want %v", got.Expirations["prod"], want)
	}
	if got.FileModTime.Unix() != 1785000000 {
		t.Fatalf("mod time: got %v (%d), want unix 1785000000", got.FileModTime, got.FileModTime.Unix())
	}
}

func TestReadCredentialExpirationsMissingFile(t *testing.T) {
	// No mtime line and no payload: the remote has no credentials file yet.
	r := &fakeRunner{out: "\n" + credentialsPayloadMarker + "\n"}
	got, err := ReadCredentialExpirations(context.Background(), r)
	if err != nil {
		t.Fatalf("ReadCredentialExpirations: %v", err)
	}
	if len(got.Expirations) != 0 {
		t.Fatalf("expected no expirations, got %v", got.Expirations)
	}
	if !got.FileModTime.IsZero() {
		t.Fatalf("expected a zero mod time, got %v", got.FileModTime)
	}
}

func TestReadCredentialExpirationsSSHFailure(t *testing.T) {
	r := &fakeRunner{out: "", err: context.DeadlineExceeded}
	if _, err := ReadCredentialExpirations(context.Background(), r); err == nil {
		t.Fatal("expected an error when the remote command fails with no output")
	}
}

func TestCredentialExpiryFor(t *testing.T) {
	exact := time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	mod := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	info := RemoteCredentialExpiry{
		Expirations: map[string]time.Time{"prod": exact},
		FileModTime: mod,
	}

	at, approx, ok := info.For("prod", 3600)
	if !ok || approx || !at.Equal(exact) {
		t.Fatalf("exact lookup: got (%v, %v, %v)", at, approx, ok)
	}

	// No recorded expiry: approximate from the file mtime plus the configured lifetime.
	at, approx, ok = info.For("legacy", 3600)
	if !ok || !approx || !at.Equal(mod.Add(time.Hour)) {
		t.Fatalf("approximate lookup: got (%v, %v, %v)", at, approx, ok)
	}

	// Duration 0 falls back to the default lifetime.
	at, approx, ok = info.For("legacy", 0)
	if !ok || !approx || !at.Equal(mod.Add(DefaultDurationSeconds*time.Second)) {
		t.Fatalf("default-duration lookup: got (%v, %v, %v)", at, approx, ok)
	}

	// Nothing to go on at all.
	empty := RemoteCredentialExpiry{}
	if _, _, ok := empty.For("prod", 0); ok {
		t.Fatal("expected no expiry when there is neither a recorded value nor a mod time")
	}
}
