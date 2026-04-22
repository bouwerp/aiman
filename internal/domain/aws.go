package domain

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
	DurationSeconds int      // credential lifetime 900–43200; 0 = AWS default
}
