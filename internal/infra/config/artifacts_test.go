package config

import (
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMarshalServeConfigCarriesArtifacts(t *testing.T) {
	cfg := &Config{Artifacts: ArtifactsConfig{Host: "aiman.fyi", AccountID: "acct", Bucket: "aiman-artifacts", Owners: []string{"a@b.com"}}}
	data, err := cfg.MarshalServeConfig("h")
	if err != nil {
		t.Fatal(err)
	}
	var got ServeConfig
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	a := got.Artifacts
	if !a.Configured() || a.Host != "aiman.fyi" || a.AccountID != "acct" || a.Bucket != "aiman-artifacts" || !slices.Equal(a.Owners, []string{"a@b.com"}) {
		t.Fatalf("artifacts %+v", a)
	}
	if (ArtifactsConfig{Host: "aiman.fyi", Bucket: "b"}).Configured() {
		t.Fatal("configured without an account id")
	}
}
