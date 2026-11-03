package gateway

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func newTestDevices(t *testing.T, now *time.Time) *DeviceStore {
	t.Helper()
	s := NewDeviceStore(filepath.Join(t.TempDir(), DevicesFileName))
	s.now = func() time.Time { return *now }
	return s
}

func TestDeviceCreateStoresOnlyHash(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s := newTestDevices(t, &now)
	dev, token, err := s.Create("iPhone", "me@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(token) {
		t.Fatalf("token is not 32 bytes of hex: %d chars", len(token))
	}
	if dev.ID == "" || dev.Name != "iPhone" || dev.Login != "me@example.com" || dev.AllowDestructive || !dev.CreatedAt.Equal(now) {
		t.Fatalf("%+v", dev)
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) || !strings.Contains(string(raw), hashHex(token)) {
		t.Fatal("token must be stored only as its sha256")
	}
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
}

func TestDeviceAuthenticate(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s := newTestDevices(t, &now)
	dev, token, err := s.Create("a", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create("b", ""); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		token  string
		wantOK bool
	}{
		{"own token", token, true},
		{"empty", "", false},
		{"wrong", strings.Repeat("0", 64), false},
		{"hash instead of token", hashHex(token), false},
		{"prefix", token[:32], false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := s.Authenticate(tt.token)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v want %v", ok, tt.wantOK)
			}
			if ok && got.ID != dev.ID {
				t.Fatalf("matched %s want %s", got.ID, dev.ID)
			}
		})
	}
}

func TestDeviceLastSeenThrottled(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s := newTestDevices(t, &now)
	_, token, err := s.Create("a", "")
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		advance time.Duration
		want    time.Time
	}{
		{0, now},
		{30 * time.Second, now},
		{59 * time.Second, now},
		{61 * time.Second, now.Add(61 * time.Second)},
		{90 * time.Second, now.Add(61 * time.Second)},
	}
	start := now
	for _, st := range steps {
		now = start.Add(st.advance)
		if _, ok := s.Authenticate(token); !ok {
			t.Fatal("auth failed")
		}
		other := NewDeviceStore(s.path)
		devs, err := other.List()
		if err != nil {
			t.Fatal(err)
		}
		if !devs[0].LastSeen.Equal(st.want) {
			t.Fatalf("at +%v last_seen %v want %v", st.advance, devs[0].LastSeen, st.want)
		}
	}
}

func TestDeviceStoreSeesOtherWriters(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	service := newTestDevices(t, &now)
	dev, token, err := service.Create("a", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := service.Authenticate(token); !ok {
		t.Fatal("auth before revoke")
	}
	cli := NewDeviceStore(service.path)
	if err := cli.SetAllowDestructive(dev.ID, true); err != nil {
		t.Fatal(err)
	}
	got, ok := service.Authenticate(token)
	if !ok || !got.AllowDestructive {
		t.Fatalf("service missed allow-destructive: %+v", got)
	}
	if err := cli.Revoke(dev.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := service.Authenticate(token); ok {
		t.Fatal("revoked token still accepted")
	}
}

func TestDeviceUnknownID(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s := newTestDevices(t, &now)
	for name, err := range map[string]error{
		"revoke": s.Revoke("nope"),
		"allow":  s.SetAllowDestructive("nope", true),
	} {
		if !errors.Is(err, ErrDeviceNotFound) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
