package awsdelegation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	plantedKey     = "AKIAPLANTEDSECRET123"
	plantedSecret  = "plantedSecretValueDoNotLeak0123456789abcd" //nolint:gosec // G101: test fixture
	plantedSession = "plantedSessionTokenDoNotLeakXYZ"
)

// plantedFile holds one section with an expiry and one without, both carrying
// recognisable fake secrets.
func plantedFile(expires time.Time) string {
	return "[with-expiry]\n" +
		"aws_access_key_id = " + plantedKey + "\n" +
		"aws_secret_access_key = " + plantedSecret + "\n" +
		"aws_session_token = " + plantedSession + "\n" +
		ExpiryKey + " = " + expires.UTC().Format(time.RFC3339) + "\n" +
		"\n[no-expiry]\n" +
		"aws_access_key_id = " + plantedKey + "\n" +
		"aws_secret_access_key = " + plantedSecret + "\n"
}

func assertNoPlanted(t *testing.T, what, s string) {
	t.Helper()
	for _, p := range []string{plantedKey, plantedSecret, plantedSession} {
		if strings.Contains(s, p) {
			t.Errorf("%s leaks planted value %q", what, p)
		}
	}
}

func TestReadHostCredentialProfiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	expires := time.Date(2026, 11, 7, 18, 30, 0, 0, time.UTC)
	if err := os.WriteFile(path, []byte(plantedFile(expires)), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	got, err := ReadHostCredentialProfiles(path)
	if err != nil {
		assertNoPlanted(t, "error", err.Error())
		t.Fatalf("read: %v", err)
	}
	byName := map[string]HostProfile{}
	for _, p := range got {
		byName[p.Name] = p
		assertNoPlanted(t, "profile", fmt.Sprintf("%+v", p))
	}
	if len(got) != 2 {
		t.Fatalf("got %d profiles, want 2: %+v", len(got), got)
	}
	w, ok := byName["with-expiry"]
	if !ok || !w.HasExpiry || !w.ExpiresAt.Equal(expires) {
		t.Errorf("with-expiry = %+v, want HasExpiry and ExpiresAt %s", w, expires)
	}
	n, ok := byName["no-expiry"]
	if !ok || n.HasExpiry || !n.ExpiresAt.IsZero() {
		t.Errorf("no-expiry = %+v, want HasExpiry false and zero ExpiresAt", n)
	}
	for _, p := range got {
		if !p.ModTime.Equal(st.ModTime()) {
			t.Errorf("%s ModTime %s, want file mtime %s", p.Name, p.ModTime, st.ModTime())
		}
	}

	missing, err := ReadHostCredentialProfiles(filepath.Join(dir, "absent"))
	if err != nil || len(missing) != 0 {
		t.Errorf("missing file: got %v, %v; want empty list and nil error", missing, err)
	}
}

func TestWriteHostCredentials(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".aws")
	path := filepath.Join(dir, "credentials")
	lock := filepath.Join(t.TempDir(), "aws-credentials.lock")
	other := "[keep-me]\naws_access_key_id = AKIAOTHERPROFILE0001\naws_secret_access_key = other\n"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	expires := time.Date(2026, 11, 8, 6, 0, 0, 0, time.UTC)
	creds := &SessionCredentials{AccessKeyID: "AKIANEW", SecretAccessKey: "s", SessionToken: "t", Expiration: expires}
	if err := WriteHostCredentials(path, lock, "phone", creds); err != nil {
		t.Fatalf("write: %v", err)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %o, want 600", st.Mode().Perm())
	}
	if ls, err := os.Stat(lock); err != nil || ls.Mode().Perm() != 0o600 {
		t.Errorf("lock file: %v, want created 0600", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "[keep-me]") || !strings.Contains(text, "AKIAOTHERPROFILE0001") {
		t.Errorf("other section lost:\n%s", text)
	}
	if !strings.Contains(text, "[phone]") || !strings.Contains(text, ExpiryKey+" = "+expires.Format(time.RFC3339)) {
		t.Errorf("new section or expiry key missing:\n%s", text)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "credentials" {
			t.Errorf("leftover file %q in credentials dir", e.Name())
		}
	}

	// A missing directory is created 0700.
	fresh := filepath.Join(t.TempDir(), "new", ".aws")
	if err := WriteHostCredentials(filepath.Join(fresh, "credentials"), lock, "phone", creds); err != nil {
		t.Fatalf("write into missing dir: %v", err)
	}
	if ds, err := os.Stat(fresh); err != nil || ds.Mode().Perm() != 0o700 {
		t.Errorf("created dir: %v, want 0700", err)
	}

	// Concurrent writers of distinct profiles all land.
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := &SessionCredentials{AccessKeyID: fmt.Sprintf("AKIACONC%02d", i), SecretAccessKey: "s", SessionToken: "t", Expiration: expires}
			errs[i] = WriteHostCredentials(path, lock, fmt.Sprintf("conc-%d", i), c)
		}()
	}
	wg.Wait()
	profiles, err := ReadHostCredentialProfiles(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	names := map[string]bool{}
	for _, p := range profiles {
		names[p.Name] = true
	}
	for i := range n {
		if errs[i] != nil {
			t.Errorf("writer %d: %v", i, errs[i])
		}
		if !names[fmt.Sprintf("conc-%d", i)] {
			t.Errorf("profile conc-%d missing after concurrent writes", i)
		}
	}
	if !names["keep-me"] || !names["phone"] {
		t.Errorf("earlier sections lost after concurrent writes: %v", names)
	}
}

func TestHostCredsNoSecretLeak(t *testing.T) {
	dir := t.TempDir()

	// A directory where the file should be.
	asDir := filepath.Join(dir, "credentials-dir")
	if err := os.MkdirAll(asDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(asDir, "inner"), []byte(plantedFile(time.Now())), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadHostCredentialProfiles(asDir)
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("read of a directory: %v, want ErrUnavailable", err)
	}
	if err != nil {
		assertNoPlanted(t, "read error (directory)", err.Error())
	}
	creds := &SessionCredentials{AccessKeyID: plantedKey, SecretAccessKey: plantedSecret, SessionToken: plantedSession}
	err = WriteHostCredentials(asDir, filepath.Join(dir, "lock"), "phone", creds)
	if err == nil {
		t.Errorf("write over a directory: %v, want an error", err)
	} else {
		assertNoPlanted(t, "write error (directory)", err.Error())
	}

	if os.Geteuid() == 0 {
		t.Log("running as root: skipping the unreadable-file case")
		return
	}
	unreadable := filepath.Join(dir, "credentials")
	if err := os.WriteFile(unreadable, []byte(plantedFile(time.Now())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })
	_, err = ReadHostCredentialProfiles(unreadable)
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("read of an unreadable file: %v, want ErrUnavailable", err)
	}
	if err != nil {
		assertNoPlanted(t, "read error (unreadable)", err.Error())
	}
	err = WriteHostCredentials(unreadable, filepath.Join(dir, "lock"), "phone", creds)
	if err == nil {
		t.Errorf("write over an unreadable file: %v, want an error", err)
	} else {
		assertNoPlanted(t, "write error (unreadable)", err.Error())
	}
}
