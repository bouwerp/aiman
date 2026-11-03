package main

import (
	"bytes"
	"encoding/json"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/gateway"
)

func fakeHost(t *testing.T, name string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	old := osHostname
	osHostname = func() (string, error) { return name, nil }
	t.Cleanup(func() { osHostname = old })
	return filepath.Join(home, ".aiman")
}

func TestParseFlagsNodeName(t *testing.T) {
	tests := []struct {
		name string
		host string
		args []string
		want string
	}{
		{"default from hostname", "DevBox.local", nil, "aiman-gateway-devbox-local"},
		{"flag overrides", "DevBox", []string{"--hostname", "custom"}, "custom"},
		{"empty hostname", "", nil, "aiman-gateway"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeHost(t, tt.host)
			opts, err := parseFlags(tt.args)
			if err != nil {
				t.Fatal(err)
			}
			if opts.hostname != tt.want {
				t.Fatalf("hostname %q want %q", opts.hostname, tt.want)
			}
		})
	}
}

func TestPairJSON(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantTarget string
	}{
		{"defaults", []string{"--json"}, "aiman-gateway-devbox:8080"},
		{"service flags", []string{"--json", "--hostname", "gw", "--port", "9000", "--name", "phone"}, "gw:9000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := fakeHost(t, "devbox")
			var out bytes.Buffer
			if err := runCLI(append([]string{"pair"}, tt.args...), &out); err != nil {
				t.Fatal(err)
			}
			var got struct {
				Target    string    `json:"target"`
				Host      string    `json:"host"`
				Code      string    `json:"code"`
				ExpiresAt time.Time `json:"expires_at"`
				URI       string    `json:"uri"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatalf("%v: %s", err, out.String())
			}
			if got.Target != tt.wantTarget || got.Host != "devbox" {
				t.Fatalf("%+v", got)
			}
			if !regexp.MustCompile(`^[A-Z2-7]{10}$`).MatchString(got.Code) {
				t.Fatalf("code %q", got.Code)
			}
			if d := time.Until(got.ExpiresAt); d < 4*time.Minute || d > 5*time.Minute {
				t.Fatalf("expires in %v", d)
			}
			u, err := url.Parse(got.URI)
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			if u.Scheme != "aimanphone" || u.Host != "pair" || q.Get("target") != got.Target || q.Get("host") != "devbox" || q.Get("code") != got.Code {
				t.Fatalf("uri %s", got.URI)
			}
			codes := gateway.NewPairCodes(filepath.Join(dir, gateway.PairCodesFileName))
			if _, ok, err := codes.Consume(got.Code); err != nil || !ok {
				t.Fatalf("minted code not redeemable: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestPairRefusesFunnel(t *testing.T) {
	fakeHost(t, "devbox")
	if err := runCLI([]string{"pair", "--funnel"}, &bytes.Buffer{}); err == nil {
		t.Fatal("pairing on Funnel must be refused")
	}
}

func TestPairText(t *testing.T) {
	fakeHost(t, "devbox")
	var out bytes.Buffer
	if err := runCLI([]string{"pair"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Code:", "Target:", "aiman-gateway-devbox:8080", "aimanphone://pair?"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestDevicesCLI(t *testing.T) {
	dir := fakeHost(t, "devbox")
	store := gateway.NewDeviceStore(filepath.Join(dir, gateway.DevicesFileName))
	dev, _, err := store.Create("iPhone", "me@example.com")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		args    []string
		wantErr bool
		wantOut []string
		check   func(t *testing.T)
	}{
		{"list", []string{"devices", "list"}, false, []string{dev.ID, "iPhone", "me@example.com"}, nil},
		{"bare devices lists", []string{"devices"}, false, []string{dev.ID}, nil},
		{"list json has no hash", []string{"devices", "list", "--json"}, false, []string{`"id"`}, nil},
		{"allow on", []string{"devices", "allow-destructive", dev.ID, "on"}, false, nil, func(t *testing.T) {
			devs, _ := store.List()
			if !devs[0].AllowDestructive {
				t.Fatal("not on")
			}
		}},
		{"allow off", []string{"devices", "allow-destructive", dev.ID, "off"}, false, nil, func(t *testing.T) {
			devs, _ := store.List()
			if devs[0].AllowDestructive {
				t.Fatal("not off")
			}
		}},
		{"allow bad value", []string{"devices", "allow-destructive", dev.ID, "maybe"}, true, nil, nil},
		{"allow missing args", []string{"devices", "allow-destructive"}, true, nil, nil},
		{"revoke unknown", []string{"devices", "revoke", "nope"}, true, nil, nil},
		{"revoke missing id", []string{"devices", "revoke"}, true, nil, nil},
		{"unknown subcommand", []string{"devices", "frob"}, true, nil, nil},
		{"revoke", []string{"devices", "revoke", dev.ID}, false, nil, func(t *testing.T) {
			devs, _ := store.List()
			if len(devs) != 0 {
				t.Fatalf("left %+v", devs)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := runCLI(tt.args, &out)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err %v", err)
			}
			for _, w := range tt.wantOut {
				if !strings.Contains(out.String(), w) {
					t.Fatalf("output lacks %q:\n%s", w, out.String())
				}
			}
			if strings.Contains(out.String(), dev.TokenHash) || strings.Contains(out.String(), "sha256") {
				t.Fatalf("output leaks the hash:\n%s", out.String())
			}
			if tt.check != nil {
				tt.check(t)
			}
		})
	}
}
