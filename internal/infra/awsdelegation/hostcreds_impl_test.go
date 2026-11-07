package awsdelegation

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validCreds() *SessionCredentials {
	return &SessionCredentials{
		AccessKeyID:     "AKIANEW",
		SecretAccessKey: "secret",
		SessionToken:    "token",
		Expiration:      time.Date(2026, 11, 8, 6, 0, 0, 0, time.UTC),
	}
}

func TestParseHostProfiles(t *testing.T) {
	content := "# top comment\r\n" +
		"orphan = ignored\r\n" +
		"[crlf]\r\n" +
		ExpiryKey + " = 2026-11-07T18:30:00Z\r\n" +
		"[bad-expiry]\n" +
		ExpiryKey + " = not-a-time\n" +
		"; " + ExpiryKey + " = 2026-11-07T18:30:00Z\n" +
		"[crlf]\n" +
		"region = us-east-1\n"
	got := parseHostProfiles(content)
	if len(got) != 2 {
		t.Fatalf("got %d profiles, want 2 (a repeated section lists once): %+v", len(got), got)
	}
	if got[0].Name != "crlf" || !got[0].HasExpiry {
		t.Errorf("crlf = %+v, want an expiry", got[0])
	}
	if got[1].Name != "bad-expiry" || got[1].HasExpiry {
		t.Errorf("bad-expiry = %+v, want no expiry", got[1])
	}
}

func TestWriteHostCredentialsRefusesInvalidInput(t *testing.T) {
	newlineInSecret := validCreds()
	newlineInSecret.SecretAccessKey = "a\n[evil]"
	noKey := validCreds()
	noKey.AccessKeyID = ""
	cases := []struct {
		name    string
		profile string
		creds   *SessionCredentials
	}{
		{"nil creds would delete the section", "phone", nil},
		{"empty key", "phone", noKey},
		{"empty profile", "", validCreds()},
		{"profile injecting a section", "a]\n[b", validCreds()},
		{"profile with surrounding space", " phone ", validCreds()},
		{"value injecting a line", "phone", newlineInSecret},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "credentials")
			original := "[keep]\naws_access_key_id = K\n"
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			err := WriteHostCredentials(path, filepath.Join(dir, "lock"), tc.profile, tc.creds)
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("err = %v, want ErrInvalidCredentials", err)
			}
			if strings.Contains(err.Error(), "evil") {
				t.Errorf("error echoes the value: %v", err)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != original {
				t.Errorf("file changed:\n%s", body)
			}
		})
	}
}

func TestWriteHostCredentialsKeepsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles-credentials")
	if err := os.WriteFile(real, []byte("[keep]\naws_access_key_id = K\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "credentials")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := WriteHostCredentials(link, filepath.Join(dir, "lock"), "phone", validCreds()); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file")
	}
	body, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "[phone]") || !strings.Contains(string(body), "[keep]") {
		t.Errorf("target file:\n%s", body)
	}
}

// TestWriteHostCredentialsReplacesSection checks a rewrite leaves one section and
// that the other sections' bytes survive.
func TestWriteHostCredentialsReplacesSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	a := "[a]\naws_access_key_id = A  \n# note in a\n"
	b := "[b]\naws_access_key_id = B\n"
	start := a + "\n[phone]\naws_access_key_id = OLD\n\n" + b
	if err := os.WriteFile(path, []byte(start), 0o644); err != nil { //nolint:gosec // G306: proves the rewrite tightens a loose mode
		t.Fatal(err)
	}
	if err := WriteHostCredentials(path, filepath.Join(dir, "lock"), "phone", validCreds()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "OLD") || strings.Count(text, "[phone]") != 1 {
		t.Errorf("old section not replaced:\n%s", text)
	}
	if !strings.Contains(text, a) || !strings.Contains(text, b) {
		t.Errorf("other sections changed:\n%s", text)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %o, want 600", st.Mode().Perm())
	}
}

// TestWriteHostCredentialsKeepsNeighbours pins each way a neighbour could be damaged.
func TestWriteHostCredentialsKeepsNeighbours(t *testing.T) {
	block := FormatCredentialsSection("phone", validCreds())
	crlfBlock := strings.ReplaceAll(block, "\n", "\r\n")
	cases := []struct{ name, existing, want string }{
		{"empty file has no aiman comment", "", block},
		{"crlf neighbour", "[keep]\r\nk = K\r\n", "[keep]\r\nk = K\r\n\r\n" + crlfBlock},
		{"case-variant neighbour", "[Phone]\nk = K\n", "[Phone]\nk = K\n\n" + block},
		{"comment above the next section", "[phone]\nk = O\n# about keep\n[keep]\nk = K\n", block + "# about keep\n[keep]\nk = K\n"},
		{"section keeps its place", "[a]\nk = A\n\n[phone]\nk = O\n\n[b]\nk = B\n", "[a]\nk = A\n\n" + block + "\n[b]\nk = B\n"},
		{"no final newline", "[keep]\nk = K", "[keep]\nk = K\n\n" + block},
		{"replaced section last without newline", "[keep]\nk = K\n\n[phone]\nk = O", "[keep]\nk = K\n\n" + block},
		{"repeat removed", "[phone]\nk = O\n\n[keep]\nk = K\n\n[phone]\nk = P\n", block + "\n[keep]\nk = K\n\n"},
		{"preamble kept", "# mine\n\n[keep]\nk = K\n", "# mine\n\n[keep]\nk = K\n\n" + block},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "credentials")
			if err := os.WriteFile(path, []byte(tc.existing), 0o600); err != nil {
				t.Fatal(err)
			}
			lock := filepath.Join(dir, "lock")
			if err := WriteHostCredentials(path, lock, "phone", validCreds()); err != nil {
				t.Fatal(err)
			}
			first, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(first) != tc.want {
				t.Errorf("got  %q\nwant %q", first, tc.want)
			}
			// A second login changes nothing: no blank-line growth.
			if err := WriteHostCredentials(path, lock, "phone", validCreds()); err != nil {
				t.Fatal(err)
			}
			second, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(second, first) {
				t.Errorf("rewrite changed the file:\n%q\n%q", first, second)
			}
		})
	}
}
