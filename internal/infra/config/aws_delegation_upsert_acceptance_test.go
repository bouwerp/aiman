package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func upsertFixture() *Config {
	return &Config{Remotes: []Remote{
		{Name: "devbox", Host: "h1", User: "u", Root: "/r",
			AWSDelegation: &AWSDelegation{Profile: "default", SourceProfile: "laptop", SyncCredentials: true, Regions: []string{"us-east-2"}},
			AWSDelegations: []*AWSDelegation{
				{Profile: "beta", SourceProfile: "beta", SyncCredentials: true, Regions: []string{"eu-west-1"}, DurationSeconds: 7200, SessionPolicy: `{"Version":"2012-10-17"}`},
			}},
		{Name: "Other", Host: "h2", User: "u", Root: "/r"},
	}}
}

func TestUpsertAWSDelegationAddsAnEntry(t *testing.T) {
	cfg := upsertFixture()
	d := AWSDelegation{Profile: "acme", SourceProfile: "acme", SyncCredentials: true, Regions: []string{"eu-west-1"},
		SSOStartURL: "https://d-123.awsapps.com/start", SSORegion: "eu-west-1", SSOAccountID: "123456789012", SSORoleName: "aiman-delegated"}
	if err := cfg.UpsertAWSDelegation("devbox", d); err != nil {
		t.Fatalf("UpsertAWSDelegation: %v", err)
	}
	r := cfg.Remotes[0]
	if len(r.AWSDelegations) != 2 || r.AWSDelegations[1].Profile != "acme" || r.AWSDelegations[1].SSOAccountID != "123456789012" {
		t.Fatalf("AWSDelegations = %+v, want beta then acme", r.AWSDelegations)
	}
	if r.AWSDelegation == nil || r.AWSDelegation.Profile != "default" {
		t.Errorf("the singular aws_delegation was touched: %+v", r.AWSDelegation)
	}
	if cfg.Remotes[1].AWSDelegations != nil {
		t.Errorf("another remote gained a delegation: %+v", cfg.Remotes[1].AWSDelegations)
	}
}

func TestUpsertAWSDelegationUpdatesByProfile(t *testing.T) {
	cfg := upsertFixture()
	d := AWSDelegation{Profile: "beta", SourceProfile: "beta-admin", SyncCredentials: true, Regions: []string{"eu-west-1", "af-south-1"},
		SSOStartURL: "https://d-456.awsapps.com/start", SSORegion: "eu-west-1", SSOAccountID: "222222222222", SSORoleName: "aiman-delegated"}
	if err := cfg.UpsertAWSDelegation("devbox", d); err != nil {
		t.Fatalf("UpsertAWSDelegation: %v", err)
	}
	r := cfg.Remotes[0]
	if len(r.AWSDelegations) != 1 {
		t.Fatalf("AWSDelegations = %d entries, want the existing one updated, not a duplicate", len(r.AWSDelegations))
	}
	got := r.AWSDelegations[0]
	if got.SourceProfile != "beta-admin" || got.SSOAccountID != "222222222222" || len(got.Regions) != 2 || got.SSOStartURL != d.SSOStartURL {
		t.Errorf("entry = %+v, want the new source profile, regions and SSO facts", got)
	}
	if got.DurationSeconds != 7200 || got.SessionPolicy == "" {
		t.Errorf("entry = %+v, want duration_seconds and session_policy kept", got)
	}
}

func TestUpsertAWSDelegationUpdatesTheSingularEntry(t *testing.T) {
	cfg := upsertFixture()
	d := AWSDelegation{Profile: "default", SourceProfile: "laptop", SyncCredentials: true, Regions: []string{"us-east-2"},
		SSOStartURL: "https://d-789.awsapps.com/start", SSORegion: "us-east-2", SSOAccountID: "333333333333", SSORoleName: "aiman-delegated"}
	if err := cfg.UpsertAWSDelegation("devbox", d); err != nil {
		t.Fatalf("UpsertAWSDelegation: %v", err)
	}
	r := cfg.Remotes[0]
	if r.AWSDelegation == nil || r.AWSDelegation.SSOAccountID != "333333333333" {
		t.Errorf("singular aws_delegation = %+v, want its SSO facts set", r.AWSDelegation)
	}
	if len(r.AWSDelegations) != 1 {
		t.Errorf("AWSDelegations = %d entries, want no duplicate of the singular profile", len(r.AWSDelegations))
	}
}

func TestUpsertAWSDelegationRefusesUnknownRemoteAndEmptyProfile(t *testing.T) {
	cfg := upsertFixture()
	err := cfg.UpsertAWSDelegation("nowhere", AWSDelegation{Profile: "acme"})
	if err == nil || !strings.Contains(err.Error(), "nowhere") || !strings.Contains(err.Error(), "devbox") || !strings.Contains(err.Error(), "Other") {
		t.Fatalf("err = %v, want one naming the unknown remote and the known ones", err)
	}
	if err := cfg.UpsertAWSDelegation("devbox", AWSDelegation{}); err == nil {
		t.Fatal("an entry without a profile name was accepted")
	}
	if len(cfg.Remotes[0].AWSDelegations) != 1 {
		t.Errorf("a refused upsert changed the list: %+v", cfg.Remotes[0].AWSDelegations)
	}
}

func TestUpsertAWSDelegationMatchesRemoteNameIgnoringCase(t *testing.T) {
	cfg := upsertFixture()
	if err := cfg.UpsertAWSDelegation("other", AWSDelegation{Profile: "acme", SourceProfile: "acme", SyncCredentials: true}); err != nil {
		t.Fatalf("UpsertAWSDelegation: %v", err)
	}
	if len(cfg.Remotes[1].AWSDelegations) != 1 {
		t.Errorf("remote Other did not get the entry: %+v", cfg.Remotes[1])
	}
}

func TestUpsertAWSDelegationRoundTripsThroughYAML(t *testing.T) {
	cfg := upsertFixture()
	d := AWSDelegation{Profile: "acme", SourceProfile: "acme", SyncCredentials: true, Regions: []string{"eu-west-1"},
		SSOStartURL: "https://d-123.awsapps.com/start", SSORegion: "eu-west-1", SSOAccountID: "123456789012", SSORoleName: "aiman-delegated"}
	if err := cfg.UpsertAWSDelegation("devbox", d); err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"aws_delegations:", "profile: acme", "sso_start_url: https://d-123.awsapps.com/start", `sso_account_id: "123456789012"`, "sso_role_name: aiman-delegated", "sync_credentials: true"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("yaml lacks %q:\n%s", want, data)
		}
	}
	var back Config
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if got := back.Remotes[0].AllDelegations(); len(got) != 3 || got[2].SSOAccountID != "123456789012" {
		t.Errorf("round trip = %+v", got)
	}
}
