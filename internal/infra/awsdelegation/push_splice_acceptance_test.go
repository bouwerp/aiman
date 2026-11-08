//go:build acceptance

package awsdelegation

import (
	"context"
	"strings"
	"testing"
	"time"
)

// spliceRemote answers the credentials cat with a fixture and records every
// WriteFile. A write to the credentials file becomes the next fixture, as on
// a real host. The config file reads as absent.
type spliceRemote struct {
	home   string
	creds  string
	writes []spliceWrite
}

type spliceWrite struct {
	path    string
	content string
}

func (f *spliceRemote) Execute(_ context.Context, cmd string) (string, error) {
	switch {
	case strings.Contains(cmd, `printf %s "$HOME"`):
		return f.home, nil
	case strings.Contains(cmd, "cat") && strings.Contains(cmd, ".aws/credentials"):
		return f.creds, nil
	default:
		return "", nil
	}
}

func (f *spliceRemote) WriteFile(_ context.Context, path string, content []byte) error {
	f.writes = append(f.writes, spliceWrite{path: path, content: string(content)})
	if strings.HasSuffix(path, "/.aws/credentials") {
		f.creds = string(content)
	}
	return nil
}

// credentialsWrites lists what was written to the credentials file.
func (f *spliceRemote) credentialsWrites() []string {
	var out []string
	for _, w := range f.writes {
		if strings.HasSuffix(w.path, "/.aws/credentials") {
			out = append(out, w.content)
		}
	}
	return out
}

func spliceCreds() *SessionCredentials {
	return &SessionCredentials{
		AccessKeyID:     "AKIANEW",
		SecretAccessKey: "secretnew",
		SessionToken:    "tokennew",
		Expiration:      time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC),
	}
}

const spliceFixture = "[Phone]\naws_access_key_id = A\naws_secret_access_key = B\n\n# keep me\n[other]\nx = 1\n"

func applyOnce(t *testing.T, f *spliceRemote, profile string, creds *SessionCredentials) string {
	t.Helper()
	before := len(f.credentialsWrites())
	if err := ApplyDelegatedCredentials(context.Background(), f, profile, creds); err != nil {
		t.Fatalf("ApplyDelegatedCredentials: %v", err)
	}
	writes := f.credentialsWrites()
	if len(writes) != before+1 {
		t.Fatalf("credentials writes = %d, want %d", len(writes), before+1)
	}
	return writes[len(writes)-1]
}

func TestApplyDelegatedCredentialsKeepsOtherCase(t *testing.T) {
	f := &spliceRemote{home: "/home/dev", creds: spliceFixture}
	creds := spliceCreds()
	got := applyOnce(t, f, "phone", creds)

	for _, keep := range []string{
		"[Phone]\naws_access_key_id = A\naws_secret_access_key = B\n",
		"\n# keep me\n[other]\nx = 1\n",
	} {
		if !strings.Contains(got, keep) {
			t.Fatalf("lost %q:\n%s", keep, got)
		}
	}
	if n := strings.Count(got, "[Phone]"); n != 1 {
		t.Fatalf("[Phone] appears %d times:\n%s", n, got)
	}
	block := FormatCredentialsSection("phone", creds)
	if !strings.Contains(got, block) {
		t.Fatalf("no [phone] block %q:\n%s", block, got)
	}
	if want := spliceCredentialsSection(spliceFixture, "phone", block); got != want {
		t.Fatalf("written file is not the splice:\ngot  %q\nwant %q", got, want)
	}
}

func TestApplyDelegatedCredentialsTwiceIsStable(t *testing.T) {
	f := &spliceRemote{home: "/home/dev", creds: spliceFixture}
	first := applyOnce(t, f, "phone", spliceCreds())
	second := applyOnce(t, f, "phone", spliceCreds())
	if first != second {
		t.Fatalf("second apply changed the file:\nfirst  %q\nsecond %q", first, second)
	}
}

func TestRemoveSessionProfileKeepsOtherCase(t *testing.T) {
	in := "[Phone]\naws_access_key_id = A\naws_secret_access_key = B\n[phone]\naws_access_key_id = C\naws_secret_access_key = D\n[other]\nx = 1\n"
	want := "[Phone]\naws_access_key_id = A\naws_secret_access_key = B\n[other]\nx = 1\n"
	f := &spliceRemote{home: "/home/dev", creds: in}
	if err := RemoveSessionProfile(context.Background(), f, "phone"); err != nil {
		t.Fatalf("RemoveSessionProfile: %v", err)
	}
	writes := f.credentialsWrites()
	if len(writes) != 1 {
		t.Fatalf("credentials writes = %d, want 1", len(writes))
	}
	if writes[0] != want {
		t.Fatalf("removed file:\ngot  %q\nwant %q", writes[0], want)
	}
}

func TestRemoveSessionProfileWithoutSectionLeavesFile(t *testing.T) {
	f := &spliceRemote{home: "/home/dev", creds: spliceFixture}
	if err := RemoveSessionProfile(context.Background(), f, "phone"); err != nil {
		t.Fatalf("RemoveSessionProfile: %v", err)
	}
	for _, w := range f.credentialsWrites() {
		if w != spliceFixture {
			t.Fatalf("file without [phone] changed:\ngot  %q\nwant %q", w, spliceFixture)
		}
	}
}

// assertCRLF fails when s holds a line feed not preceded by a carriage return.
func assertCRLF(t *testing.T, s string) {
	t.Helper()
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' && (i == 0 || s[i-1] != '\r') {
			t.Fatalf("bare LF at byte %d:\n%q", i, s)
		}
	}
}

func TestApplyDelegatedCredentialsKeepsCRLF(t *testing.T) {
	in := strings.ReplaceAll(spliceFixture, "\n", "\r\n")
	f := &spliceRemote{home: "/home/dev", creds: in}
	got := applyOnce(t, f, "phone", spliceCreds())
	assertCRLF(t, got)
	if !strings.Contains(got, "[Phone]\r\naws_access_key_id = A\r\naws_secret_access_key = B\r\n") {
		t.Fatalf("lost [Phone]:\n%q", got)
	}
}

func TestRemoveSessionProfileKeepsCRLF(t *testing.T) {
	in := "[Phone]\r\naws_access_key_id = A\r\n[phone]\r\naws_access_key_id = C\r\n[other]\r\nx = 1\r\n"
	want := "[Phone]\r\naws_access_key_id = A\r\n[other]\r\nx = 1\r\n"
	f := &spliceRemote{home: "/home/dev", creds: in}
	if err := RemoveSessionProfile(context.Background(), f, "phone"); err != nil {
		t.Fatalf("RemoveSessionProfile: %v", err)
	}
	writes := f.credentialsWrites()
	if len(writes) != 1 || writes[0] != want {
		t.Fatalf("removed file:\ngot  %q\nwant %q", writes, want)
	}
}
