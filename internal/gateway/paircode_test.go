package gateway

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestPairCodes(t *testing.T, now *time.Time) *PairCodes {
	t.Helper()
	p := NewPairCodes(filepath.Join(t.TempDir(), PairCodesFileName))
	p.now = func() time.Time { return *now }
	return p
}

func TestPairCodeMintFormatAndStorage(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	p := newTestPairCodes(t, &now)
	code, exp, err := p.Mint("phone")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[A-Z2-7]{10}$`).MatchString(code) {
		t.Fatalf("code %q is not 10 base32 chars", code)
	}
	if !exp.Equal(now.Add(PairCodeTTL)) || PairCodeTTL != 5*time.Minute {
		t.Fatalf("expires %v", exp)
	}
	info, err := os.Stat(p.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	raw, err := os.ReadFile(p.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), code) {
		t.Fatal("code must be stored only as a hash")
	}
	if !strings.Contains(string(raw), hashHex(code)) {
		t.Fatalf("file has no hash: %s", raw)
	}
}

func TestPairCodeConsume(t *testing.T) {
	tests := []struct {
		name    string
		advance time.Duration
		input   func(code string) string
		twice   bool
		wantOK  bool
	}{
		{"valid", 0, func(c string) string { return c }, false, true},
		{"lowercase and spaced", 0, func(c string) string { return " " + strings.ToLower(c[:5]) + "-" + c[5:] + " " }, false, true},
		{"just before expiry", PairCodeTTL - time.Second, func(c string) string { return c }, false, true},
		{"expired", PairCodeTTL, func(c string) string { return c }, false, false},
		{"wrong", 0, func(string) string { return "AAAAAAAAAA" }, false, false},
		{"empty", 0, func(string) string { return "" }, false, false},
		{"single use", 0, func(c string) string { return c }, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
			p := newTestPairCodes(t, &now)
			code, _, err := p.Mint("hint")
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(tt.advance)
			in := tt.input(code)
			if tt.twice {
				if _, ok, err := p.Consume(in); err != nil || !ok {
					t.Fatalf("first consume ok=%v err=%v", ok, err)
				}
			}
			name, ok, err := p.Consume(in)
			if err != nil {
				t.Fatal(err)
			}
			if ok != tt.wantOK {
				t.Fatalf("ok=%v want %v", ok, tt.wantOK)
			}
			if ok && name != "hint" {
				t.Fatalf("name %q", name)
			}
		})
	}
}

func TestPairCodeExpiredArePruned(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	p := newTestPairCodes(t, &now)
	if _, _, err := p.Mint(""); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if _, _, err := p.Mint(""); err != nil {
		t.Fatal(err)
	}
	codes, err := p.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 1 {
		t.Fatalf("kept %d codes", len(codes))
	}
}

func TestPairCodeConcurrentMintsKeepEveryCode(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), PairCodesFileName)
	var wg sync.WaitGroup
	codes := make([]string, 8)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Separate values stand in for the CLI and the service.
			p := NewPairCodes(path)
			p.now = func() time.Time { return now }
			c, _, err := p.Mint("")
			if err != nil {
				t.Error(err)
			}
			codes[i] = c
		}(i)
	}
	wg.Wait()
	p := NewPairCodes(path)
	p.now = func() time.Time { return now }
	for _, c := range codes {
		if _, ok, err := p.Consume(c); err != nil || !ok {
			t.Fatalf("code lost: ok=%v err=%v", ok, err)
		}
	}
}
