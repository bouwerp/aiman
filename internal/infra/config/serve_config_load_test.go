package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Serve reads its config through Load, so what MarshalServeConfig pushes must
// survive a decode into Config, not only into ServeConfig.
func TestLoadKeepsPushedAWSDelegations(t *testing.T) {
	include := []string{"laptop-only-profile"}
	laptop := &Config{
		AWS: AWSDefaults{IncludeProfiles: &include},
		Remotes: []Remote{{
			Host: "h1",
			AWSDelegation: &AWSDelegation{
				Profile: "default", AccountID: "111111111111", SyncCredentials: true,
				Regions: []string{"us-east-2"}, SSOStartURL: "https://corp.awsapps.com/start",
				SSORegion: "eu-west-1", SSOAccountID: "111111111111", SSORoleName: "aiman-delegated",
			},
		}},
	}
	body, err := laptop.MarshalServeConfig("h1")
	if err != nil {
		t.Fatalf("MarshalServeConfig: %v", err)
	}
	if strings.Contains(string(body), "laptop-only-profile") {
		t.Fatalf("laptop AWS defaults reached serve:\n%s", body)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, DirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []ServeAWSDelegation{{
		Profile: "default", AccountID: "111111111111", Regions: []string{"us-east-2"},
		SSOStartURL: "https://corp.awsapps.com/start", SSORegion: "eu-west-1",
		SSOAccountID: "111111111111", SSORoleName: "aiman-delegated",
	}}
	if !reflect.DeepEqual(got.AWS.Delegations, want) {
		t.Fatalf("Load AWS.Delegations = %+v\nwant %+v", got.AWS.Delegations, want)
	}
}

// aws.delegations on a laptop config is not a source: the push lists only the
// delegations of the remote it targets.
func TestMarshalServeConfigIgnoresLaptopAWSDelegations(t *testing.T) {
	laptop := &Config{
		AWS:     AWSDefaults{Delegations: []ServeAWSDelegation{{Profile: "stray"}}},
		Remotes: []Remote{{Host: "h1"}},
	}
	body, err := laptop.MarshalServeConfig("h1")
	if err != nil {
		t.Fatalf("MarshalServeConfig: %v", err)
	}
	if strings.Contains(string(body), "stray") {
		t.Fatalf("a laptop aws.delegations entry reached serve:\n%s", body)
	}
}
