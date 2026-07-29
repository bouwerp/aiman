package domain

import "strings"

// LegacyScopedAWSProfilePrefix is the prefix of the per-session AWS profiles aiman
// wrote before v0.8.11 (e.g. "aiman-58f485ff"). Those profiles are no longer created
// in ~/.aws, so a stored name that still carries this prefix points at a profile that
// does not exist: every `aws` call in the session fails with
// "The config profile (aiman-…) could not be found".
const LegacyScopedAWSProfilePrefix = "aiman-"

// IsLegacyScopedAWSProfile reports whether name is one of the dead session-scoped
// profiles described above.
func IsLegacyScopedAWSProfile(name string) bool {
	return strings.HasPrefix(strings.TrimSpace(name), LegacyScopedAWSProfilePrefix)
}

// AWSConfig holds per-session AWS credential configuration.
// Fields mirror config.AWSDelegation but are defined here in domain
// to avoid a dependency on the infra/config package.
type AWSConfig struct {
	SourceProfile   string   // local AWS profile used to call STS
	RoleName        string   // IAM role name; empty = "TemporaryDelegatedRole"
	AccountID       string   // 12-digit account ID; derived from SourceProfile if empty
	Region          string   // written into the remote AWS profile as "region"
	Regions         []string // restrict via aws:RequestedRegion condition policy
	SessionPolicy   string   // inline JSON IAM policy passed to sts assume-role
	DurationSeconds int      // credential lifetime 900–43200; 0 = DefaultDurationSeconds (43200)
}
