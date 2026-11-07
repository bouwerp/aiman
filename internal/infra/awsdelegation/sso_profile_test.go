//go:build acceptance

package awsdelegation

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

const ssoConfigFixture = `[default]
sso_start_url = https://legacy.awsapps.com/start
sso_region = us-west-2
sso_account_id = 111111111111
sso_role_name = Admin
region = us-east-1

[profile dev]
sso_session = corp
sso_account_id = 222222222222
sso_role_name=PowerUser
region = eu-west-1

[sso-session corp]
sso_start_url = https://corp.awsapps.com/start
sso_region = eu-west-1
sso_registration_scopes = sso:account:access

[profile legacy]
sso_start_url = https://old.awsapps.com/start
sso_region = ap-southeast-2
sso_account_id = 333333333333
sso_role_name = ReadOnly

[profile plain]
region = us-east-1
output = json
`

func writeSSOConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(ssoConfigFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadSSOProfile(t *testing.T) {
	path := writeSSOConfig(t)

	cases := []struct {
		name    string
		profile string
		want    SSOProfile
	}{
		{"sso_session form", "dev", SSOProfile{
			StartURL: "https://corp.awsapps.com/start", Region: "eu-west-1",
			AccountID: "222222222222", RoleName: "PowerUser",
		}},
		{"legacy inline form", "legacy", SSOProfile{
			StartURL: "https://old.awsapps.com/start", Region: "ap-southeast-2",
			AccountID: "333333333333", RoleName: "ReadOnly",
		}},
		{"default profile", "default", SSOProfile{
			StartURL: "https://legacy.awsapps.com/start", Region: "us-west-2",
			AccountID: "111111111111", RoleName: "Admin",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ReadSSOProfile(path, c.profile)
			if err != nil {
				t.Fatalf("ReadSSOProfile(%q): %v", c.profile, err)
			}
			if got != c.want {
				t.Fatalf("ReadSSOProfile(%q) = %+v, want %+v", c.profile, got, c.want)
			}
		})
	}

	t.Run("no SSO keys", func(t *testing.T) {
		_, err := ReadSSOProfile(path, "plain")
		if !errors.Is(err, ErrNoSSO) {
			t.Fatalf("err = %v, want ErrNoSSO", err)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := ReadSSOProfile(filepath.Join(t.TempDir(), "absent"), "dev")
		if err == nil || errors.Is(err, ErrNoSSO) {
			t.Fatalf("err = %v, want a file error, not ErrNoSSO", err)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("err = %v, want it to wrap fs.ErrNotExist", err)
		}
	})
}
