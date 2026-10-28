package config

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestAgentUpdateYAMLRoundTrip(t *testing.T) {
	in := []byte(`agents:
  update:
    mode: notify
    window: "22:00-02:00"
    check_every: 3h
    hold: [muse, ageni]
`)
	var cfg Config
	if err := yaml.Unmarshal(in, &cfg); err != nil {
		t.Fatal(err)
	}
	want := AgentUpdateConfig{Mode: "notify", Window: "22:00-02:00", CheckEvery: 3 * time.Hour, Hold: []string{"muse", "ageni"}}
	if !reflect.DeepEqual(cfg.Agents.Update, want) {
		t.Fatalf("update = %+v, want %+v", cfg.Agents.Update, want)
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Agents.Update, want) {
		t.Fatalf("round trip = %+v\n%s", back.Agents.Update, out)
	}
	if back.Agents.Preferred != nil {
		t.Fatalf("preferred appeared from nowhere: %v\n%s", back.Agents.Preferred, out)
	}
}

func TestAgentUpdateDefaults(t *testing.T) {
	var u AgentUpdateConfig
	if u.ModeOrDefault() != "auto" || u.WindowOrDefault() != "03:00-06:00" || u.CheckEveryOrDefault() != 6*time.Hour {
		t.Fatalf("defaults: %q %q %s", u.ModeOrDefault(), u.WindowOrDefault(), u.CheckEveryOrDefault())
	}
	u = AgentUpdateConfig{Mode: "off", Window: "01:00-02:00", CheckEvery: time.Hour}
	if u.ModeOrDefault() != "off" || u.WindowOrDefault() != "01:00-02:00" || u.CheckEveryOrDefault() != time.Hour {
		t.Fatalf("explicit: %q %q %s", u.ModeOrDefault(), u.WindowOrDefault(), u.CheckEveryOrDefault())
	}
}

func TestAgentsConfigIsZeroAccountsForUpdate(t *testing.T) {
	if !(AgentsConfig{}).IsZero() {
		t.Fatal("empty agents block should be zero")
	}
	if (AgentsConfig{Update: AgentUpdateConfig{Mode: "off"}}).IsZero() {
		t.Fatal("agents block with update set should not be zero")
	}
	if (AgentsConfig{Update: AgentUpdateConfig{Hold: []string{"muse"}}}).IsZero() {
		t.Fatal("agents block with hold set should not be zero")
	}
	body, err := yaml.Marshal(Config{Agents: AgentsConfig{Update: AgentUpdateConfig{Mode: "off"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "mode: \"off\"") && !strings.Contains(string(body), "mode: off") {
		t.Fatalf("update not written:\n%s", body)
	}
	if strings.Contains(string(body), "preferred") {
		t.Fatalf("unset preferred written:\n%s", body)
	}
	cleared, err := yaml.Marshal(Config{Agents: AgentsConfig{Preferred: PreferredFromCommands()}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cleared), "preferred: []") {
		t.Fatalf("cleared preferred not written:\n%s", cleared)
	}
}

func TestMarshalServeConfigCarriesAgentUpdate(t *testing.T) {
	cfg := &Config{Agents: AgentsConfig{Update: AgentUpdateConfig{Mode: "auto", Window: "02:00-04:00", CheckEvery: 2 * time.Hour, Hold: []string{"codex"}}}}
	body, err := cfg.MarshalServeConfig("h")
	if err != nil {
		t.Fatal(err)
	}
	var got ServeConfig
	if err := yaml.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Agents.Update, cfg.Agents.Update) {
		t.Fatalf("update = %+v, want %+v\n%s", got.Agents.Update, cfg.Agents.Update, body)
	}
}
