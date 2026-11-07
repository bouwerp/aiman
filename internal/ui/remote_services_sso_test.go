package ui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/bouwerp/aiman/internal/infra/config"
)

const laptopAWSConfig = `[profile laptop-src]
sso_session = corp
sso_account_id = 111111111111
sso_role_name = OperatorAdmin

[sso-session corp]
sso_start_url = https://corp.awsapps.com/start
sso_region = eu-west-1

[profile by-name]
sso_start_url = https://inline.awsapps.com/start
sso_region = us-west-2
sso_account_id = 222222222222
`

func writeLaptopAWSConfig(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if body == "" {
		return
	}
	dir := filepath.Join(home, ".aws")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func pushedDelegations(t *testing.T, cfg *config.Config) (string, []config.ServeAWSDelegation) {
	t.Helper()
	writer := &recordingServeConfigWriter{}
	if err := writeServeConfig(context.Background(), cfg, "h1", writer); err != nil {
		t.Fatalf("writeServeConfig: %v", err)
	}
	var got config.ServeConfig
	if err := yaml.Unmarshal(writer.content, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return string(writer.content), got.AWS.Delegations
}

func TestWriteServeConfigFillsSSOFactsFromLaptopProfile(t *testing.T) {
	writeLaptopAWSConfig(t, laptopAWSConfig)
	single := &config.AWSDelegation{Profile: "default", SourceProfile: "laptop-src", SyncCredentials: true}
	listed := &config.AWSDelegation{Profile: "by-name", SyncCredentials: true}
	unsynced := &config.AWSDelegation{Profile: "unsynced", SourceProfile: "laptop-src"}
	cfg := &config.Config{Remotes: []config.Remote{{
		Host:           "h1",
		AWSDelegation:  single,
		AWSDelegations: []*config.AWSDelegation{listed, unsynced},
	}}}

	body, got := pushedDelegations(t, cfg)

	want := []config.ServeAWSDelegation{
		{
			Profile: "default", SSOStartURL: "https://corp.awsapps.com/start",
			SSORegion: "eu-west-1", SSOAccountID: "111111111111",
		},
		{
			Profile: "by-name", SSOStartURL: "https://inline.awsapps.com/start",
			SSORegion: "us-west-2", SSOAccountID: "222222222222",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delegations = %+v\nwant %+v", got, want)
	}
	for _, banned := range []string{"OperatorAdmin", "laptop-src", "unsynced"} {
		if strings.Contains(body, banned) {
			t.Errorf("pushed config carries %q:\n%s", banned, body)
		}
	}
	if single.SSOStartURL != "" || listed.SSOStartURL != "" || cfg.Remotes[0].AWSDelegations[0] != listed {
		t.Fatalf("the laptop's config was changed: %+v %+v", single, listed)
	}
}

func TestWriteServeConfigExplicitSSOFactsWin(t *testing.T) {
	writeLaptopAWSConfig(t, laptopAWSConfig)
	cfg := &config.Config{Remotes: []config.Remote{{
		Host: "h1",
		AWSDelegation: &config.AWSDelegation{
			Profile: "default", SourceProfile: "laptop-src", SyncCredentials: true,
			SSOStartURL: "https://mine.awsapps.com/start", SSORoleName: "aiman-delegated",
		},
	}}}

	_, got := pushedDelegations(t, cfg)

	want := []config.ServeAWSDelegation{{
		Profile: "default", SSOStartURL: "https://mine.awsapps.com/start", SSORegion: "eu-west-1",
		SSOAccountID: "111111111111", SSORoleName: "aiman-delegated",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delegations = %+v\nwant %+v", got, want)
	}
}

func TestWriteServeConfigPushesDelegationWithoutFactsOnReadFailure(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"no aws config file", ""},
		{"profile without sso keys", "[profile laptop-src]\nregion = us-east-1\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeLaptopAWSConfig(t, c.body)
			cfg := &config.Config{Remotes: []config.Remote{{
				Host:          "h1",
				AWSDelegation: &config.AWSDelegation{Profile: "default", SourceProfile: "laptop-src", SyncCredentials: true},
			}}}

			_, got := pushedDelegations(t, cfg)

			want := []config.ServeAWSDelegation{{Profile: "default"}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("delegations = %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestConfigWithSSOFactsLeavesOtherHostsAlone(t *testing.T) {
	writeLaptopAWSConfig(t, laptopAWSConfig)
	other := &config.AWSDelegation{Profile: "by-name", SyncCredentials: true}
	cfg := &config.Config{Remotes: []config.Remote{{Host: "h2", AWSDelegation: other}}}

	out := configWithSSOFacts(cfg, "h1")

	if out.Remotes[0].AWSDelegation != other || other.SSOStartURL != "" {
		t.Fatalf("another host's delegation changed: %+v", out.Remotes[0].AWSDelegation)
	}
	if configWithSSOFacts(nil, "h1") != nil {
		t.Fatal("nil config must stay nil")
	}
}
