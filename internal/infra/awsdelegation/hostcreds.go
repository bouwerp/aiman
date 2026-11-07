package awsdelegation

import (
	"errors"
	"time"
)

// ErrUnavailable means the host credentials file exists but cannot be read.
var ErrUnavailable = errors.New("aws credentials file unavailable")

// errHostCredsNotImplemented is returned until P2a lands.
var errHostCredsNotImplemented = errors.New("awsdelegation: host credentials not implemented")

// HostProfile is what the host credentials file says about one section. It never
// carries key values.
type HostProfile struct {
	Name      string
	ExpiresAt time.Time
	HasExpiry bool
	ModTime   time.Time
}

// ReadHostCredentialProfiles parses the credentials file at path.
func ReadHostCredentialProfiles(path string) ([]HostProfile, error) {
	return nil, errHostCredsNotImplemented
}

// WriteHostCredentials merges one profile's section into the credentials file at path.
func WriteHostCredentials(path, lockPath, profile string, creds *SessionCredentials) error {
	return errHostCredsNotImplemented
}
