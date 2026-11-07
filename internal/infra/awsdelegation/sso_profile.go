package awsdelegation

import "errors"

// ErrNoSSO is returned when a profile carries no SSO keys.
var ErrNoSSO = errors.New("profile has no SSO configuration")

// SSOProfile holds the non-secret SSO facts of one AWS CLI profile.
type SSOProfile struct {
	StartURL  string
	Region    string
	AccountID string
	RoleName  string
}

// ReadSSOProfile reads the SSO facts for profile from the AWS CLI config file
// at configPath.
func ReadSSOProfile(configPath, profile string) (SSOProfile, error) {
	return SSOProfile{}, errors.New("ReadSSOProfile: not implemented")
}
