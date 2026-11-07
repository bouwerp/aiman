//go:build acceptance

package config

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
)

var serveAWSDelegationKeys = []string{
	"account_id", "duration_seconds", "profile", "regions", "role_name",
	"session_policy", "sso_account_id", "sso_region", "sso_role_name", "sso_start_url",
}

func awsServeFixture() *Config {
	return &Config{Remotes: []Remote{
		{
			Name: "one", Host: "h1", User: "u",
			AWSDelegation: &AWSDelegation{
				Profile: "default", AccountID: "111111111111", RoleName: "TemporaryDelegatedRole",
				SourceProfile: "laptop-src-profile", SyncCredentials: true, ManagedRole: true,
				Region: "us-east-2", Regions: []string{"us-east-2", "eu-west-1"},
				SessionPolicy: `{"Version":"2012-10-17"}`, DurationSeconds: 43200,
				SSOStartURL: "https://corp.awsapps.com/start", SSORegion: "eu-west-1",
				SSOAccountID: "111111111111", SSORoleName: "aiman-delegated",
			},
			AWSDelegations: []*AWSDelegation{
				{Profile: "prod-ro", AccountID: "999999999999", SourceProfile: "laptop-prod", SyncCredentials: false},
				{Profile: "staging", AccountID: "222222222222", SourceProfile: "laptop-staging", SyncCredentials: true, Regions: []string{"us-west-2"}},
			},
		},
		{
			Name: "two", Host: "h2", User: "u",
			AWSDelegation: &AWSDelegation{Profile: "elsewhere", AccountID: "333333333333", SyncCredentials: true},
		},
	}}
}

func TestMarshalServeConfigAWS(t *testing.T) {
	body, err := awsServeFixture().MarshalServeConfig("h1")
	if err != nil {
		t.Fatalf("MarshalServeConfig: %v", err)
	}

	for _, banned := range []string{
		"laptop-src-profile", "laptop-prod", "laptop-staging", "source_profile",
		"sync_credentials", "managed_role", "prod-ro", "elsewhere",
	} {
		if strings.Contains(string(body), banned) {
			t.Errorf("serve config carries %q:\n%s", banned, body)
		}
	}

	var raw struct {
		AWS struct {
			Delegations []map[string]any `yaml:"delegations"`
		} `yaml:"aws"`
	}
	if err := yaml.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if len(raw.AWS.Delegations) != 2 {
		t.Fatalf("aws.delegations = %v, want the two synced delegations of h1:\n%s", raw.AWS.Delegations, body)
	}
	full := raw.AWS.Delegations[0]
	keys := make([]string, 0, len(full))
	for k := range full {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, serveAWSDelegationKeys) {
		t.Fatalf("fully set delegation keys = %v, want exactly %v", keys, serveAWSDelegationKeys)
	}
	for _, d := range raw.AWS.Delegations[1:] {
		for k := range d {
			if !containsString(serveAWSDelegationKeys, k) {
				t.Errorf("delegation carries unexpected key %q", k)
			}
		}
	}

	var got ServeConfig
	if err := yaml.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal ServeConfig: %v", err)
	}
	want := []ServeAWSDelegation{
		{
			Profile: "default", AccountID: "111111111111", RoleName: "TemporaryDelegatedRole",
			Regions: []string{"us-east-2", "eu-west-1"}, SessionPolicy: `{"Version":"2012-10-17"}`,
			DurationSeconds: 43200, SSOStartURL: "https://corp.awsapps.com/start", SSORegion: "eu-west-1",
			SSOAccountID: "111111111111", SSORoleName: "aiman-delegated",
		},
		{Profile: "staging", AccountID: "222222222222", Regions: []string{"us-west-2"}},
	}
	if !reflect.DeepEqual(got.AWS.Delegations, want) {
		t.Fatalf("ServeConfig.AWS.Delegations = %+v\nwant %+v", got.AWS.Delegations, want)
	}

	again, err := yaml.Marshal(got)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	var round ServeConfig
	if err := yaml.Unmarshal(again, &round); err != nil {
		t.Fatalf("unmarshal round trip: %v", err)
	}
	if !reflect.DeepEqual(round.AWS, got.AWS) {
		t.Fatalf("AWS section does not round-trip: %+v vs %+v", round.AWS, got.AWS)
	}

	other, err := awsServeFixture().MarshalServeConfig("nohost")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(other), "delegations") {
		t.Fatalf("a host with no delegations must get no aws.delegations:\n%s", other)
	}
}

func TestAWSDelegationOldConfigRoundTrip(t *testing.T) {
	old := "profile: default\naccount_id: \"111111111111\"\nsource_profile: me\nsync_credentials: true\nregions:\n    - us-east-2\n"
	var d AWSDelegation
	if err := yaml.Unmarshal([]byte(old), &d); err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != old {
		t.Fatalf("old delegation does not round-trip:\n got %q\nwant %q", out, old)
	}
}

func TestFillSSOFactsNeverSetsRoleName(t *testing.T) {
	p := awsdelegation.SSOProfile{
		StartURL: "https://corp.awsapps.com/start", Region: "eu-west-1",
		AccountID: "111111111111", RoleName: "OperatorAdmin",
	}

	var empty AWSDelegation
	FillSSOFacts(&empty, p)
	if empty.SSOStartURL != p.StartURL || empty.SSORegion != p.Region || empty.SSOAccountID != p.AccountID {
		t.Fatalf("empty facts not filled: %+v", empty)
	}
	if empty.SSORoleName != "" {
		t.Fatalf("SSORoleName = %q, must never be inferred from the portal role", empty.SSORoleName)
	}

	set := AWSDelegation{
		SSOStartURL: "https://mine.awsapps.com/start", SSORegion: "us-east-1",
		SSOAccountID: "222222222222", SSORoleName: "aiman-delegated",
	}
	before := set
	FillSSOFacts(&set, p)
	if !reflect.DeepEqual(set, before) {
		t.Fatalf("set values overwritten: %+v, want %+v", set, before)
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
