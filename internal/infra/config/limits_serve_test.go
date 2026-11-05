package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMarshalServeConfigCarriesLimits(t *testing.T) {
	cfg := &Config{Limits: LimitsConfig{ClaudeStatusline: true}}
	body, err := cfg.MarshalServeConfig("h")
	if err != nil {
		t.Fatal(err)
	}
	var back ServeConfig
	if err := yaml.Unmarshal(body, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Limits.ClaudeStatusline {
		t.Fatalf("limits lost:\n%s", body)
	}
}
