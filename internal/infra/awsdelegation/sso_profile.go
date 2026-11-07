package awsdelegation

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

// ErrNoSSO is returned when a profile carries no SSO keys.
var ErrNoSSO = errors.New("profile has no SSO configuration")

// SSOProfile holds the non-secret SSO facts of one AWS CLI profile.
type SSOProfile struct {
	StartURL  string
	Region    string
	AccountID string
	RoleName  string
}

const (
	ssoKeySession   = "sso_session"
	ssoKeyStartURL  = "sso_start_url"
	ssoKeyRegion    = "sso_region"
	ssoKeyAccountID = "sso_account_id"
	ssoKeyRoleName  = "sso_role_name"
)

// ssoKeys is everything ReadSSOProfile keeps from the file. Any other value,
// which in a config file can include static credentials, is never retained.
var ssoKeys = []string{ssoKeySession, ssoKeyStartURL, ssoKeyRegion, ssoKeyAccountID, ssoKeyRoleName}

// ReadSSOProfile reads the SSO facts for profile from the AWS CLI config file
// at configPath. It accepts both the sso_session form, whose start URL and
// region live in an [sso-session NAME] section, and the legacy inline form.
// A profile with no SSO keys, absent or not, is ErrNoSSO.
func ReadSSOProfile(configPath, profile string) (SSOProfile, error) {
	data, err := os.ReadFile(configPath) //nolint:gosec // G304: the caller names the AWS config file; only SSO keys are kept
	if err != nil {
		return SSOProfile{}, fmt.Errorf("reading AWS config: %w", err)
	}
	sections := parseSSOSections(string(data))

	section := profileSection(sections, strings.TrimSpace(profile))
	if len(section) == 0 {
		return SSOProfile{}, fmt.Errorf("profile %q: %w", profile, ErrNoSSO)
	}
	out := SSOProfile{
		StartURL:  section[ssoKeyStartURL],
		Region:    section[ssoKeyRegion],
		AccountID: section[ssoKeyAccountID],
		RoleName:  section[ssoKeyRoleName],
	}
	if name := section[ssoKeySession]; name != "" {
		session, ok := sections["sso-session "+name]
		if !ok {
			return SSOProfile{}, fmt.Errorf("profile %q names missing sso-session %q: %w", profile, name, ErrNoSSO)
		}
		out.StartURL = session[ssoKeyStartURL]
		out.Region = session[ssoKeyRegion]
	}
	return out, nil
}

// profileSection finds a profile's section. The AWS CLI accepts default as
// [profile default] or [default] and prefers the former.
func profileSection(sections map[string]map[string]string, profile string) map[string]string {
	if section, ok := sections["profile "+profile]; ok {
		return section
	}
	if profile == "default" {
		return sections["default"]
	}
	return nil
}

// parseSSOSections maps each section header, with its whitespace normalised, to
// the SSO keys set in it.
func parseSSOSections(data string) map[string]map[string]string {
	sections := map[string]map[string]string{}
	var current map[string]string
	for line := range strings.Lines(data) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			header, _, _ := strings.Cut(line[1:], "]")
			header = strings.Join(strings.Fields(header), " ")
			if sections[header] == nil {
				sections[header] = map[string]string{}
			}
			current = sections[header]
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if ok && current != nil && slices.Contains(ssoKeys, key) {
			current[key] = strings.TrimSpace(value)
		}
	}
	return sections
}
