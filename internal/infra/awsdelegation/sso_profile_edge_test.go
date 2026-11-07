package awsdelegation

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func readSSOFromString(t *testing.T, body, profile string) (SSOProfile, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return ReadSSOProfile(path, profile)
}

func TestReadSSOProfileEdges(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		profile string
		want    SSOProfile
		wantErr error
	}{
		{
			name:    "absent profile",
			body:    "[profile other]\nsso_region = eu-west-1\n",
			profile: "dev",
			wantErr: ErrNoSSO,
		},
		{
			name:    "credentials section name is not a profile",
			body:    "[dev]\nsso_region = eu-west-1\n",
			profile: "dev",
			wantErr: ErrNoSSO,
		},
		{
			name:    "sso-session of the same name is not the profile",
			body:    "[sso-session dev]\nsso_region = eu-west-1\n",
			profile: "dev",
			wantErr: ErrNoSSO,
		},
		{
			name:    "missing sso-session section",
			body:    "[profile dev]\nsso_session = gone\nsso_account_id = 1\n",
			profile: "dev",
			wantErr: ErrNoSSO,
		},
		{
			name:    "commented keys do not count",
			body:    "[profile dev]\n# sso_region = eu-west-1\n; sso_start_url = x\nregion = us-east-1\n",
			profile: "dev",
			wantErr: ErrNoSSO,
		},
		{
			name:    "profile default wins over default",
			body:    "[default]\nsso_region = a\n[profile default]\nsso_region = b\n",
			profile: "default",
			want:    SSOProfile{Region: "b"},
		},
		{
			name:    "extra whitespace and a comment after the header",
			body:    "[profile   dev] # mine\n  sso_region  =  eu-west-1  \n[profile next]\nsso_region = other\n",
			profile: "dev",
			want:    SSOProfile{Region: "eu-west-1"},
		},
		{
			name:    "keys of the next section stay there",
			body:    "[profile dev]\nsso_region = a\n[profile next]\nsso_account_id = 1\n",
			profile: "dev",
			want:    SSOProfile{Region: "a"},
		},
		{
			name:    "sso_session section wins over inline values",
			body:    "[profile dev]\nsso_session = s\nsso_region = inline\n[sso-session s]\nsso_region = session\nsso_start_url = u\n",
			profile: "dev",
			want:    SSOProfile{Region: "session", StartURL: "u"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := readSSOFromString(t, c.body, c.profile)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}
