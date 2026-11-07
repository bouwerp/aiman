package config

import "github.com/bouwerp/aiman/internal/infra/awsdelegation"

// FillSSOFacts fills empty SSO start URL, region and account id on d from p.
// Explicit values win. SSORoleName is never filled: it names the delegated
// permission set, and p.RoleName is the operator's own portal role.
func FillSSOFacts(d *AWSDelegation, p awsdelegation.SSOProfile) {
	if d == nil {
		return
	}
	if d.SSOStartURL == "" {
		d.SSOStartURL = p.StartURL
	}
	if d.SSORegion == "" {
		d.SSORegion = p.Region
	}
	if d.SSOAccountID == "" {
		d.SSOAccountID = p.AccountID
	}
}
