package config

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const hostUpdateYAML = "host:\n  update:\n    mode: notify\n    check_every: 5m\n"

func TestHostUpdateConfigLoads(t *testing.T) {
	cfg := loadConfigFile(t, hostUpdateYAML)
	u := cfg.Host.Update
	if u.Mode != "notify" || u.CheckEvery != 5*time.Minute {
		t.Fatalf("Host.Update = %+v, want notify every 5m", u)
	}
	if u.ModeOrDefault() != "notify" || u.CheckEveryOrDefault() != 5*time.Minute {
		t.Fatalf("defaults applied over set values: %q %v", u.ModeOrDefault(), u.CheckEveryOrDefault())
	}
}

func TestHostUpdateConfigRoundTrips(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte(hostUpdateYAML), &cfg); err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatalf("re-read: %v\n%s", err, out)
	}
	if back.Host.Update.Mode != "notify" || back.Host.Update.CheckEvery != 5*time.Minute {
		t.Fatalf("host.update lost on write: %+v\n%s", back.Host.Update, out)
	}
}

func TestHostUpdateConfigDefaults(t *testing.T) {
	tests := []struct {
		name  string
		cfg   HostUpdateConfig
		mode  string
		every time.Duration
	}{
		{"empty", HostUpdateConfig{}, "auto", 10 * time.Minute},
		{"bogus mode", HostUpdateConfig{Mode: "bogus"}, "notify", 10 * time.Minute},
		{"off", HostUpdateConfig{Mode: "off"}, "off", 10 * time.Minute},
		{"auto", HostUpdateConfig{Mode: "auto"}, "auto", 10 * time.Minute},
		{"below floor", HostUpdateConfig{CheckEvery: 10 * time.Second}, "auto", time.Minute},
		{"at floor", HostUpdateConfig{CheckEvery: time.Minute}, "auto", time.Minute},
		{"set", HostUpdateConfig{CheckEvery: time.Hour}, "auto", time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ModeOrDefault(); got != tt.mode {
				t.Errorf("ModeOrDefault = %q, want %q", got, tt.mode)
			}
			if got := tt.cfg.CheckEveryOrDefault(); got != tt.every {
				t.Errorf("CheckEveryOrDefault = %v, want %v", got, tt.every)
			}
		})
	}
}

func TestHostUpdateConfigFromYAMLDefaults(t *testing.T) {
	cfg := loadConfigFile(t, "host:\n  update:\n    mode: bogus\n    check_every: 10s\n")
	if got := cfg.Host.Update.ModeOrDefault(); got != "notify" {
		t.Fatalf("mode bogus: ModeOrDefault = %q, want notify", got)
	}
	if got := cfg.Host.Update.CheckEveryOrDefault(); got != time.Minute {
		t.Fatalf("check_every 10s: CheckEveryOrDefault = %v, want 1m", got)
	}
}

func TestMarshalServeConfigCarriesHostUpdate(t *testing.T) {
	cfg := loadConfigFile(t, hostUpdateYAML)
	body, err := cfg.MarshalServeConfig("h1")
	if err != nil {
		t.Fatalf("MarshalServeConfig: %v", err)
	}
	if !strings.Contains(string(body), "host:") || !strings.Contains(string(body), "update:") {
		t.Fatalf("serve config has no host.update block:\n%s", body)
	}
	var serve ServeConfig
	if err := yaml.Unmarshal(body, &serve); err != nil {
		t.Fatalf("unmarshal serve config: %v", err)
	}
	if serve.Host.Update.Mode != "notify" || serve.Host.Update.CheckEvery != 5*time.Minute {
		t.Fatalf("serve Host.Update = %+v, want notify every 5m\n%s", serve.Host.Update, body)
	}
}
