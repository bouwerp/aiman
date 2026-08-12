package config

import "testing"

func TestResolveAWSSessionDefaults(t *testing.T) {
	delegation := &AWSDelegation{SourceProfile: "delegation-profile", Region: "us-east-2"}

	tests := []struct {
		name        string
		cfg         *Config
		remote      Remote
		delegation  *AWSDelegation
		wantProfile string
		wantRegion  string
	}{
		{
			name:        "falls back to the delegation when nothing is configured",
			cfg:         &Config{},
			remote:      Remote{},
			delegation:  delegation,
			wantProfile: "delegation-profile",
			wantRegion:  "us-east-2",
		},
		{
			name:        "global default beats the delegation",
			cfg:         &Config{AWS: AWSDefaults{DefaultProfile: "global", DefaultRegion: "eu-west-1"}},
			remote:      Remote{},
			delegation:  delegation,
			wantProfile: "global",
			wantRegion:  "eu-west-1",
		},
		{
			name:        "remote override beats the global default",
			cfg:         &Config{AWS: AWSDefaults{DefaultProfile: "global", DefaultRegion: "eu-west-1"}},
			remote:      Remote{AWSDefaultProfile: "remote", AWSDefaultRegion: "ap-south-1"},
			delegation:  delegation,
			wantProfile: "remote",
			wantRegion:  "ap-south-1",
		},
		{
			name:        "profile and region resolve independently",
			cfg:         &Config{AWS: AWSDefaults{DefaultProfile: "global"}},
			remote:      Remote{AWSDefaultRegion: "ap-south-1"},
			delegation:  delegation,
			wantProfile: "global",
			wantRegion:  "ap-south-1",
		},
		{
			name:        "whitespace-only values do not override",
			cfg:         &Config{AWS: AWSDefaults{DefaultProfile: "   ", DefaultRegion: "\t"}},
			remote:      Remote{AWSDefaultProfile: " "},
			delegation:  delegation,
			wantProfile: "delegation-profile",
			wantRegion:  "us-east-2",
		},
		{
			name:        "nil delegation with a configured default",
			cfg:         &Config{AWS: AWSDefaults{DefaultProfile: "global"}},
			remote:      Remote{},
			delegation:  nil,
			wantProfile: "global",
			wantRegion:  "",
		},
		{
			name:        "nil config falls back to the delegation",
			cfg:         nil,
			remote:      Remote{},
			delegation:  delegation,
			wantProfile: "delegation-profile",
			wantRegion:  "us-east-2",
		},
		{
			name:        "remote override applies even with a nil config",
			cfg:         nil,
			remote:      Remote{AWSDefaultProfile: "remote"},
			delegation:  delegation,
			wantProfile: "remote",
			wantRegion:  "us-east-2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotProfile, gotRegion := tt.cfg.ResolveAWSSessionDefaults(tt.remote, tt.delegation)
			if gotProfile != tt.wantProfile {
				t.Errorf("profile = %q, want %q", gotProfile, tt.wantProfile)
			}
			if gotRegion != tt.wantRegion {
				t.Errorf("region = %q, want %q", gotRegion, tt.wantRegion)
			}
		})
	}
}

// The resolver must not mutate the delegation it reads from, since the same
// pointer is shared by every session created against that remote.
func TestResolveAWSSessionDefaultsDoesNotMutateDelegation(t *testing.T) {
	d := &AWSDelegation{SourceProfile: "original", Region: "us-east-2"}
	cfg := &Config{AWS: AWSDefaults{DefaultProfile: "global", DefaultRegion: "eu-west-1"}}

	cfg.ResolveAWSSessionDefaults(Remote{AWSDefaultProfile: "remote"}, d)

	if d.SourceProfile != "original" || d.Region != "us-east-2" {
		t.Fatalf("delegation was mutated: %+v", d)
	}
}
