package awsdelegation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PermissionSetSessionDuration is the delegated permission set's session
// duration: the 12-hour sessions the phone sign-in hands to the host.
const PermissionSetSessionDuration = "PT12H"

const (
	defaultApplyPollInterval = 2 * time.Second
	defaultApplyPollTimeout  = 2 * time.Minute
	// assignmentConflictRetries is how often a create-account-assignment that
	// answers ConflictException is tried again before the listings decide.
	assignmentConflictRetries = 3
	permissionSetDescription  = "aiman delegated sessions"

	statusSucceeded  = "SUCCEEDED"
	statusFailed     = "FAILED"
	statusInProgress = "IN_PROGRESS"
)

// AWSCLI runs one aws invocation and returns its stdout.
type AWSCLI interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// ExecAWSCLI runs the aws binary under one profile with JSON output.
type ExecAWSCLI struct{ Profile string }

// Run executes the command and returns its stdout alone, so a warning the CLI
// prints on stderr cannot break the JSON. On a non-zero exit the error is
// "aws <service> <subcommand>: <first line of stderr, else of stdout>", since
// the CLI writes "An error occurred (...)" to stderr.
func (e ExecAWSCLI) Run(ctx context.Context, args ...string) ([]byte, error) {
	args = e.stripProfile(args)
	cmd := e.command(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, runError(args, stdout.String(), stderr.String(), err)
	}
	return stdout.Bytes(), nil
}

// stripProfile drops a leading --profile <e.Profile>: Apply passes it on every
// call so a fake can check it, and command already adds it.
func (e ExecAWSCLI) stripProfile(args []string) []string {
	if len(args) >= 2 && args[0] == "--profile" && args[1] == e.Profile {
		return args[2:]
	}
	return args
}

// runError is a failed call's error: the first line of stderr, else of stdout,
// else err itself. An "Unknown options:" line wins over the usage text the CLI
// prints above it, so a caller can tell a flag the CLI predates.
func runError(args []string, stdout, stderr string, err error) error {
	msg := unknownOptionsLine(stderr)
	if msg == "" {
		msg = firstLine(stderr)
	}
	if msg == "" {
		msg = firstLine(stdout)
	}
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("aws %s: %s", awsOperation(args), msg)
}

// unknownOptionsLine is the CLI's "Unknown options: ..." line, or empty.
func unknownOptionsLine(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "Unknown options:") {
			return line
		}
	}
	return ""
}

// command builds aws --profile <p> --output json <args...>.
func (e ExecAWSCLI) command(ctx context.Context, args ...string) *exec.Cmd {
	full := append([]string{"--profile", e.Profile, "--output", "json"}, args...)
	return exec.CommandContext(ctx, "aws", full...) // #nosec G204 -- fixed binary, arguments built by Apply
}

// ApplySpec is what permission-set-apply sets up.
type ApplySpec struct {
	Profile, AccountID, Name, User string
	Regions                        []string
	AWSConfigPath                  string        // empty: ~/.aws/config (os.UserHomeDir)
	PollInterval, PollTimeout      time.Duration // zero: 2s and 2m
	// SSORegion and StartURL override discovery and the derived access portal
	// URL for an admin profile without SSO keys; a profile with SSO keys
	// ignores them with a warning.
	SSORegion, StartURL string
	// VerifyStartURL checks the start URL of a profile without SSO keys
	// against the regional OIDC endpoint; nil skips the check.
	VerifyStartURL func(ctx context.Context, region, startURL string) error
}

// ErrStartURLRejected is a VerifyStartURL answer: the OIDC endpoint refused
// the start URL. awslogin returns this value.
var ErrStartURLRejected = errors.New("oidc rejected the start url")

// DelegationHint is the remotes[].aws_delegation lines for the account.
type DelegationHint struct {
	StartURL  string `json:"sso_start_url"`
	Region    string `json:"sso_region"`
	AccountID string `json:"sso_account_id"`
	RoleName  string `json:"sso_role_name"`
}

// ApplyResult is what Apply found or created.
type ApplyResult struct {
	InstanceARN          string         `json:"instance_arn"`
	IdentityStoreID      string         `json:"identity_store_id"`
	PermissionSetARN     string         `json:"permission_set_arn"`
	PrincipalID          string         `json:"principal_id"`
	UserName             string         `json:"user_name"`
	AccountID            string         `json:"account_id"`
	CreatedPermissionSet bool           `json:"created_permission_set"`
	CreatedAssignment    bool           `json:"created_assignment"`
	Delegation           DelegationHint `json:"delegation"`
}

// ApplyStep is one planned aws call.
type ApplyStep struct {
	Name string
	Args []string // full aws argument list, --profile included
}

// applyRun carries one Apply's state between its steps.
type applyRun struct {
	ctx    context.Context
	cli    AWSCLI
	spec   ApplySpec
	log    io.Writer
	region string
	res    ApplyResult
	// profile is the admin profile's SSO section when it has one; noSSO is
	// true only when ReadSSOProfile answered ErrNoSSO.
	profile SSOProfile
	noSSO   bool
	// home is the partition's home region, for calls that need one before
	// the Identity Center region is known.
	home string
	// owner is the instance's OwnerAccountId; callerAccount is the admin
	// profile's own account from get-caller-identity.
	owner, callerAccount string
	// callerARN is the caller's ARN from get-caller-identity; its UserId is
	// never kept.
	callerARN string
	// provisioned is true once a create in this run provisioned the set.
	provisioned bool
}

// normalizeSpec fills defaults and checks the regions.
func normalizeSpec(spec ApplySpec) (ApplySpec, error) {
	if PermissionSetPolicy(spec.Regions) == "" {
		return spec, errors.New("no regions: pass --regions a,b or set them on a delegation")
	}
	if spec.Name = strings.TrimSpace(spec.Name); spec.Name == "" {
		spec.Name = DefaultPermissionSetName
	}
	if spec.PollInterval <= 0 {
		spec.PollInterval = defaultApplyPollInterval
	}
	if spec.PollTimeout <= 0 {
		spec.PollTimeout = defaultApplyPollTimeout
	}
	if spec.AWSConfigPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			spec.AWSConfigPath = filepath.Join(home, ".aws", "config")
		}
	}
	return spec, nil
}

// readProfile is step 0: the admin profile's SSO section, and noSSO when the
// profile has no SSO keys. Any other failure is a warning line on log and
// neither: an unreadable config is never mistaken for a plain IAM profile.
func readProfile(spec ApplySpec, log io.Writer) (p SSOProfile, noSSO bool) {
	if spec.AWSConfigPath == "" {
		_, _ = fmt.Fprintf(log, "warning: no home directory to find ~/.aws/config; Identity Center calls use the profile's default region\n")
		return SSOProfile{}, false
	}
	p, err := ReadSSOProfile(spec.AWSConfigPath, spec.Profile)
	if errors.Is(err, ErrNoSSO) {
		return SSOProfile{}, true
	}
	if err != nil {
		_, _ = fmt.Fprintf(log, "warning: cannot read sso_region of profile %s: %v; Identity Center calls use the profile's default region\n", spec.Profile, err)
		return SSOProfile{}, false
	}
	if spec.SSORegion != "" || spec.StartURL != "" {
		_, _ = fmt.Fprintf(log, "warning: profile %s has SSO keys; --sso-region and --start-url are ignored\n", spec.Profile)
	}
	return p, false
}

// Apply creates or updates the delegated permission set, puts its inline
// policy, assigns it to the user for the account and provisions it when the
// assignment already existed. Each step logs one line to log.
func Apply(ctx context.Context, cli AWSCLI, spec ApplySpec, log io.Writer) (ApplyResult, error) {
	spec, err := normalizeSpec(spec)
	if err != nil {
		return ApplyResult{}, err
	}
	r := &applyRun{ctx: ctx, cli: cli, spec: spec, log: log}
	r.profile, r.noSSO = readProfile(spec, log)
	r.region = r.profile.Region
	for _, step := range r.steps() {
		if err := step(); err != nil {
			return r.res, err
		}
	}
	return r.res, nil
}

// steps is the step order. A profile without SSO keys asks sts first, for the
// partition discovery needs, then discovers the instance and checks its type.
func (r *applyRun) steps() []func() error {
	rest := []func() error{r.permissionSet, r.policy, r.principal, r.assignment, r.provision}
	if !r.noSSO {
		return append(append([]func() error{r.instance, r.account}, rest...), r.hint)
	}
	return append(append([]func() error{r.account, r.partition, r.discover, r.instanceType, r.organization}, rest...), r.discoveredHint)
}

// logf writes one progress line.
func (r *applyRun) logf(format string, a ...any) {
	_, _ = fmt.Fprintf(r.log, format+"\n", a...)
}

// callArgs builds an aws argument list: --profile, then --region <region> on
// Identity Center calls (sso-admin, identitystore) and --region <home> on
// account calls when those are set, then the operation. sts and organizations
// get none.
func callArgs(profile, region, home string, op ...string) []string {
	out := []string{"--profile", profile}
	if len(op) > 0 {
		switch op[0] {
		case "sso-admin", "identitystore":
			if region != "" {
				out = append(out, "--region", region)
			}
		case "account":
			if home != "" {
				out = append(out, "--region", home)
			}
		}
	}
	return append(out, op...)
}

// call runs one aws invocation in the run's Identity Center region and
// decodes its JSON into v when v is not nil.
func (r *applyRun) call(v any, op ...string) error {
	return r.callIn(r.region, v, op...)
}

// callIn is call with an explicit Identity Center region.
func (r *applyRun) callIn(region string, v any, op ...string) error {
	args := callArgs(r.spec.Profile, region, r.home, op...)
	out, err := r.cli.Run(r.ctx, args...)
	if err != nil {
		return wrapCLIError(args, err)
	}
	if v == nil || strings.TrimSpace(string(out)) == "" {
		return nil
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("aws %s: parse output: %w", awsOperation(args), err)
	}
	return nil
}

// wrapCLIError gives err the "aws <service> <subcommand>: <first line>" form
// unless ExecAWSCLI already did.
func wrapCLIError(args []string, err error) error {
	prefix := "aws " + awsOperation(args) + ": "
	msg := firstLine(err.Error())
	if strings.HasPrefix(msg, prefix) {
		return errors.New(msg)
	}
	return errors.New(prefix + msg)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

// awsOperation returns "<service> <subcommand>" of an argument list, skipping
// global flags and their values.
func awsOperation(args []string) string {
	var words []string
	for i := 0; i < len(args) && len(words) < 2; i++ {
		if strings.HasPrefix(args[i], "--") {
			i++
			continue
		}
		words = append(words, args[i])
	}
	return strings.Join(words, " ")
}

// instanceRecord is one ListInstances or DescribeInstance row.
type instanceRecord struct {
	InstanceArn     string `json:"InstanceArn"`
	IdentityStoreID string `json:"IdentityStoreId"`
	OwnerAccountID  string `json:"OwnerAccountId"`
	PrimaryRegion   string `json:"PrimaryRegion"`
	Regions         []struct {
		RegionName      string `json:"RegionName"`
		IsPrimaryRegion bool   `json:"IsPrimaryRegion"`
	} `json:"Regions"`
}

// primary is the Region the instance was enabled in: PrimaryRegion, else the
// Regions[] entry marked primary, else empty.
func (in instanceRecord) primary() string {
	if in.PrimaryRegion != "" {
		return in.PrimaryRegion
	}
	for _, reg := range in.Regions {
		if reg.IsPrimaryRegion && reg.RegionName != "" {
			return reg.RegionName
		}
	}
	return ""
}

// listInstances runs sso-admin list-instances in region with extra flags.
func (r *applyRun) listInstances(region string, extra ...string) ([]instanceRecord, error) {
	var out struct {
		Instances []instanceRecord `json:"Instances"`
	}
	err := r.callIn(region, &out, append([]string{"sso-admin", "list-instances"}, extra...)...)
	return out.Instances, err
}

// oneInstance applies the one-instance rule to a non-empty list.
func (r *applyRun) oneInstance(ins []instanceRecord) (instanceRecord, error) {
	if len(ins) == 1 {
		return ins[0], nil
	}
	arns := make([]string, 0, len(ins))
	for _, in := range ins {
		arns = append(arns, in.InstanceArn)
	}
	return instanceRecord{}, fmt.Errorf("profile %s sees %d Identity Center instances (%s); expected one", r.spec.Profile, len(arns), strings.Join(arns, ", "))
}

// useInstance records the instance the rest of the run works on.
func (r *applyRun) useInstance(in instanceRecord) {
	r.res.InstanceARN = in.InstanceArn
	r.res.IdentityStoreID = in.IdentityStoreID
	r.owner = in.OwnerAccountID
	r.logf("instance %s (identity store %s)", r.res.InstanceARN, r.res.IdentityStoreID)
}

// instance is step 1: exactly one Identity Center instance in r.region.
func (r *applyRun) instance() error {
	ins, err := r.listInstances(r.region)
	if err != nil {
		return err
	}
	if len(ins) == 0 {
		region := r.region
		if region == "" {
			region = "the profile's default region"
		}
		return fmt.Errorf("no Identity Center instance visible to profile %s in %s; check the profile's sso_region", r.spec.Profile, region)
	}
	in, err := r.oneInstance(ins)
	if err != nil {
		return err
	}
	r.useInstance(in)
	return nil
}

// account is step 2: the target account, and the caller ARN step 5 reads.
func (r *applyRun) account() error {
	var out struct {
		Account string `json:"Account"`
		Arn     string `json:"Arn"`
	}
	if err := r.call(&out, "sts", "get-caller-identity"); err != nil {
		return err
	}
	r.callerARN = out.Arn
	r.callerAccount = out.Account
	if r.spec.AccountID != "" {
		r.res.AccountID = r.spec.AccountID
		r.logf("account %s", r.res.AccountID)
		return nil
	}
	if out.Account == "" {
		return errors.New("aws sts get-caller-identity: no account in the answer; pass --account")
	}
	r.res.AccountID = out.Account
	r.logf("account %s (the admin profile's own account)", r.res.AccountID)
	return nil
}

// awsPartitionHome is the aws partition's home region, where ListInstances
// with ALL_REGIONS and account list-regions are asked.
const awsPartitionHome = "us-east-1"

// partitionHome is the home region of the caller ARN's partition. Only the aws
// partition is supported: the access portal URL shape and the OIDC endpoint
// the host signs in through are commercial-only.
func partitionHome(arn string) (string, bool) {
	if strings.HasPrefix(arn, "arn:aws:") {
		return awsPartitionHome, true
	}
	return "", false
}

// partition is the check after step 2 for a profile without SSO keys.
func (r *applyRun) partition() error {
	home, ok := partitionHome(r.callerARN)
	if !ok {
		return fmt.Errorf("phone sign-in supports the aws partition only (caller %s)", r.callerARN)
	}
	r.home = home
	return nil
}

// discover is steps 0 and 1 for a profile without SSO keys: the Identity
// Center region and instance, from --sso-region, else ListInstances with
// ALL_REGIONS, else the region scan when the CLI predates --region-scope or
// the answer names no region.
func (r *applyRun) discover() error {
	if r.spec.SSORegion != "" {
		r.region = r.spec.SSORegion
		if err := r.instance(); err != nil {
			return err
		}
		r.logf("Identity Center region %s (--sso-region)", r.region)
		return nil
	}
	ins, err := r.listInstances(r.home, "--region-scope", "ALL_REGIONS")
	if isUnknownRegionScope(err) {
		return r.scan()
	}
	if err != nil {
		return err
	}
	if len(ins) == 0 {
		return fmt.Errorf("no Identity Center instance visible to profile %s in any region; %s", r.spec.Profile, enableInstanceHint)
	}
	in, err := r.oneInstance(ins)
	if err != nil {
		return err
	}
	if in.primary() == "" {
		return r.scan()
	}
	r.region = in.primary()
	r.useInstance(in)
	r.logf("Identity Center region %s (all regions)", r.region)
	return nil
}

// enableInstanceHint is the console path to an organization instance.
const enableInstanceHint = "enable IAM Identity Center (an organization instance) in the console, add your user with an email invitation, then rerun"

// isUnknownRegionScope reports the CLI rejecting --region-scope as an
// unknown option, which an AWS CLI older than the parameter does.
func isUnknownRegionScope(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Unknown options") && strings.Contains(err.Error(), "region-scope")
}

// scan asks list-instances in every region the account has enabled, never
// stopping early, and applies the one-instance rule to the union. A failing
// region counts as empty; the first failure's line is kept for the error.
func (r *applyRun) scan() error {
	regions, err := r.accountRegions()
	if err != nil {
		return fmt.Errorf("cannot list the account's regions (%s); pass --sso-region <region of the Identity Center instance>, or update the AWS CLI", firstLine(err.Error()))
	}
	var found []instanceRecord
	regionOf := map[string]string{}
	var kept string
	for _, region := range regions {
		ins, err := r.listInstances(region)
		if err != nil {
			if kept == "" {
				kept = firstLine(err.Error())
			}
			continue
		}
		for _, in := range ins {
			if _, seen := regionOf[in.InstanceArn]; !seen {
				regionOf[in.InstanceArn] = region
				found = append(found, in)
			}
		}
	}
	if len(found) == 0 {
		msg := fmt.Sprintf("no Identity Center instance in any of %d regions for profile %s; %s; or pass --sso-region if the instance is in a region this profile cannot list", len(regions), r.spec.Profile, enableInstanceHint)
		if kept != "" {
			msg += " (" + kept + ")"
		}
		return errors.New(msg)
	}
	in, err := r.oneInstance(found)
	if err != nil {
		return err
	}
	r.region = in.primary()
	if r.region == "" {
		r.region = regionOf[in.InstanceArn]
	}
	r.useInstance(in)
	r.logf("Identity Center region %s (scanned %d regions)", r.region, len(regions))
	return nil
}

// accountRegions is the account's enabled regions from account list-regions.
func (r *applyRun) accountRegions() ([]string, error) {
	var out struct {
		Regions []struct {
			RegionName string `json:"RegionName"`
		} `json:"Regions"`
	}
	if err := r.call(&out, "account", "list-regions", "--region-opt-status-contains", "ENABLED", "ENABLED_BY_DEFAULT"); err != nil {
		return nil, err
	}
	regions := make([]string, 0, len(out.Regions))
	for _, reg := range out.Regions {
		if reg.RegionName != "" {
			regions = append(regions, reg.RegionName)
		}
	}
	return regions, nil
}

// instanceType is step 1b(a): an instance with permission sets disabled
// cannot hold the set. A payload without PermissionSetsEnabled passes.
func (r *applyRun) instanceType() error {
	var out struct {
		OwnerAccountID        string `json:"OwnerAccountId"`
		PermissionSetsEnabled *bool  `json:"PermissionSetsEnabled"`
	}
	if err := r.call(&out, "sso-admin", "describe-instance", "--instance-arn", r.res.InstanceARN); err != nil {
		return err
	}
	if out.PermissionSetsEnabled != nil && !*out.PermissionSetsEnabled {
		return fmt.Errorf("instance %s has permission sets disabled: it is an account instance, or an organization instance with multi-account permissions turned off, "+
			"so it cannot hold %s; enable IAM Identity Center as an organization instance from the management account "+
			"(an account that enabled Identity Center before creating its organization keeps an account instance), add your user with an email invitation, then rerun",
			r.res.InstanceARN, r.spec.Name)
	}
	if r.owner == "" {
		r.owner = out.OwnerAccountID
	}
	return nil
}

// organization is step 1b(b): the instance must belong to the organization's
// management account. AccessDenied skips the check with a warning.
func (r *applyRun) organization() error {
	var out struct {
		Organization struct {
			MasterAccountID string `json:"MasterAccountId"`
		} `json:"Organization"`
	}
	err := r.call(&out, "organizations", "describe-organization")
	switch {
	case err != nil && strings.Contains(err.Error(), "AWSOrganizationsNotInUseException"):
		return fmt.Errorf("account %s is not in an organization, so %s is an account instance, which cannot hold permission sets; "+
			"run aws organizations create-organization --feature-set ALL --profile %s to make it a management account, "+
			"enable IAM Identity Center (organization instance) in the console, add your user with an email invitation, then rerun",
			r.callerAccount, r.res.InstanceARN, r.spec.Profile)
	case err != nil && strings.Contains(err.Error(), "AccessDeniedException"):
		r.logf("warning: cannot describe the organization (%s); skipping the instance owner check", firstLine(err.Error()))
		return nil
	case err != nil:
		return err
	}
	master := out.Organization.MasterAccountID
	if r.owner != "" && master != "" && r.owner != master {
		return fmt.Errorf("instance %s is owned by account %s, not the organization's management account %s: an account instance cannot hold permission sets; "+
			"enable the organization instance from the management account", r.res.InstanceARN, r.owner, master)
	}
	return nil
}

type permissionSetRecord struct {
	PermissionSet struct {
		PermissionSetArn string `json:"PermissionSetArn"`
		Name             string `json:"Name"`
		SessionDuration  string `json:"SessionDuration"`
	} `json:"PermissionSet"`
}

// permissionSet is step 3: find the set by name, fix its duration, or create it.
func (r *applyRun) permissionSet() error {
	found, err := r.findPermissionSet()
	if err != nil {
		return err
	}
	i := r.res.InstanceARN
	if found == nil {
		var created permissionSetRecord
		if err := r.call(&created, "sso-admin", "create-permission-set", "--instance-arn", i,
			"--name", r.spec.Name, "--session-duration", PermissionSetSessionDuration,
			"--description", permissionSetDescription); err != nil {
			return err
		}
		r.res.PermissionSetARN = created.PermissionSet.PermissionSetArn
		r.res.CreatedPermissionSet = true
		r.logf("permission set %s created (%s)", r.spec.Name, r.res.PermissionSetARN)
		return nil
	}
	r.res.PermissionSetARN = found.PermissionSet.PermissionSetArn
	if found.PermissionSet.SessionDuration == PermissionSetSessionDuration {
		r.logf("permission set %s exists (%s)", r.spec.Name, r.res.PermissionSetARN)
		return nil
	}
	if err := r.call(nil, "sso-admin", "update-permission-set", "--instance-arn", i,
		"--permission-set-arn", r.res.PermissionSetARN, "--session-duration", PermissionSetSessionDuration); err != nil {
		return err
	}
	r.logf("permission set %s session duration %s -> %s", r.spec.Name, found.PermissionSet.SessionDuration, PermissionSetSessionDuration)
	return nil
}

// findPermissionSet describes each permission set until one has spec.Name.
func (r *applyRun) findPermissionSet() (*permissionSetRecord, error) {
	var list struct {
		PermissionSets []string `json:"PermissionSets"`
	}
	if err := r.call(&list, "sso-admin", "list-permission-sets", "--instance-arn", r.res.InstanceARN); err != nil {
		return nil, err
	}
	for _, arn := range list.PermissionSets {
		var rec permissionSetRecord
		if err := r.call(&rec, "sso-admin", "describe-permission-set", "--instance-arn", r.res.InstanceARN, "--permission-set-arn", arn); err != nil {
			return nil, err
		}
		if rec.PermissionSet.Name == r.spec.Name {
			if rec.PermissionSet.PermissionSetArn == "" {
				rec.PermissionSet.PermissionSetArn = arn
			}
			return &rec, nil
		}
	}
	return nil, nil
}

// policy is step 4: put the region-locked inline policy, every run.
func (r *applyRun) policy() error {
	if err := r.call(nil, "sso-admin", "put-inline-policy-to-permission-set", "--instance-arn", r.res.InstanceARN,
		"--permission-set-arn", r.res.PermissionSetARN, "--inline-policy", PermissionSetPolicy(r.spec.Regions)); err != nil {
		return err
	}
	r.logf("inline policy put for regions %s", strings.Join(r.spec.Regions, ","))
	return nil
}

// ssoSessionUser returns the session name of an Identity Center role session,
// assumed-role/AWSReservedSSO_<ps>_<hash>/<session>, which is the user name.
func ssoSessionUser(callerARN string) (string, bool) {
	parts := strings.SplitN(callerARN, ":", 6)
	if len(parts) != 6 {
		return "", false
	}
	res := strings.Split(parts[5], "/")
	if len(res) != 3 || res[0] != "assumed-role" || !strings.HasPrefix(res[1], "AWSReservedSSO_") || res[2] == "" {
		return "", false
	}
	return res[2], true
}

// addUserHint ends the step 5 errors with the console path to a new user.
const addUserHint = " (Users → Add user in the IAM Identity Center console, with an email invitation, when it does not exist yet)"

// principal is step 5: the Identity Center user id of spec.User or the caller.
func (r *applyRun) principal() error {
	user := strings.TrimSpace(r.spec.User)
	if user == "" {
		u, ok := ssoSessionUser(r.callerARN)
		if !ok {
			return fmt.Errorf("cannot infer your Identity Center user name from %s; pass --user <Identity Center user name>%s", r.callerARN, addUserHint)
		}
		user = u
	}
	alt, err := json.Marshal(map[string]any{"UniqueAttribute": map[string]string{"AttributePath": "userName", "AttributeValue": user}})
	if err != nil {
		return err
	}
	var out struct {
		UserID string `json:"UserId"`
	}
	err = r.call(&out, "identitystore", "get-user-id", "--identity-store-id", r.res.IdentityStoreID, "--alternate-identifier", string(alt))
	if err != nil {
		if strings.Contains(err.Error(), "ResourceNotFoundException") {
			return fmt.Errorf("no user %s in identity store %s; pass --user <Identity Center user name>%s", user, r.res.IdentityStoreID, addUserHint)
		}
		return err
	}
	r.res.UserName = user
	r.res.PrincipalID = out.UserID
	r.logf("user %s", user)
	return nil
}

// assignmentListed reports whether the principal holds the set in the account.
func (r *applyRun) assignmentListed() (bool, error) {
	var out struct {
		AccountAssignments []struct {
			PrincipalID string `json:"PrincipalId"`
		} `json:"AccountAssignments"`
	}
	if err := r.call(&out, "sso-admin", "list-account-assignments", "--instance-arn", r.res.InstanceARN,
		"--account-id", r.res.AccountID, "--permission-set-arn", r.res.PermissionSetARN); err != nil {
		return false, err
	}
	for _, a := range out.AccountAssignments {
		if a.PrincipalID == r.res.PrincipalID {
			return true, nil
		}
	}
	return false, nil
}

// requestStatus is the shape of an assignment creation or provisioning status.
type requestStatus struct {
	RequestID     string `json:"RequestId"`
	Status        string `json:"Status"`
	FailureReason string `json:"FailureReason"`
	// TargetID, PermissionSetARN and PrincipalID are set on an assignment
	// creation status only.
	TargetID         string `json:"TargetId"`
	PermissionSetARN string `json:"PermissionSetArn"`
	PrincipalID      string `json:"PrincipalId"`
}

// assignment is step 6: assign the set to the principal for the account.
func (r *applyRun) assignment() error {
	listed, err := r.assignmentListed()
	if err != nil {
		return err
	}
	if listed {
		r.logf("assignment for %s in account %s exists", r.res.UserName, r.res.AccountID)
		return nil
	}
	st, err := r.createAssignment()
	if isConflict(err) {
		return r.resolveConflict(err)
	}
	if err != nil {
		return err
	}
	r.logf("assignment for %s in account %s requested (%s)", r.res.UserName, r.res.AccountID, st.RequestID)
	if err := r.settleAssignment(st); err != nil {
		return err
	}
	r.res.CreatedAssignment = true
	r.provisioned = true
	return nil
}

// createAssignment calls create-account-assignment, retrying a conflict.
func (r *applyRun) createAssignment() (requestStatus, error) {
	var err error
	for attempt := 0; attempt <= assignmentConflictRetries; attempt++ {
		if attempt > 0 {
			if werr := r.wait(); werr != nil {
				return requestStatus{}, werr
			}
		}
		var out struct {
			Status requestStatus `json:"AccountAssignmentCreationStatus"`
		}
		err = r.call(&out, "sso-admin", "create-account-assignment", "--instance-arn", r.res.InstanceARN,
			"--target-type", "AWS_ACCOUNT", "--target-id", r.res.AccountID, "--permission-set-arn", r.res.PermissionSetARN,
			"--principal-type", "USER", "--principal-id", r.res.PrincipalID)
		if !isConflict(err) {
			return out.Status, err
		}
	}
	return requestStatus{}, err
}

func isConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "ConflictException")
}

// resolveConflict reads a lasting ConflictException: a listed assignment is
// present, an in-flight create is waited for, and anything else is conflictErr.
func (r *applyRun) resolveConflict(conflictErr error) error {
	listed, err := r.assignmentListed()
	if err != nil {
		return err
	}
	if listed {
		r.logf("assignment for %s in account %s exists (create answered ConflictException)", r.res.UserName, r.res.AccountID)
		return nil
	}
	st, found, err := r.ourInFlightCreate()
	if err != nil {
		return err
	}
	if !found {
		return conflictErr
	}
	r.logf("waiting for in-flight assignment request %s", st.RequestID)
	if err := r.settleAssignment(st); err != nil {
		return err
	}
	r.res.CreatedAssignment = true
	r.provisioned = true
	return nil
}

// ourInFlightCreate finds an IN_PROGRESS assignment creation for this
// account, permission set and principal. The listing's rows carry no target,
// so each is described once; the first matching status is returned.
func (r *applyRun) ourInFlightCreate() (requestStatus, bool, error) {
	var out struct {
		Rows []struct {
			RequestID string `json:"RequestId"`
		} `json:"AccountAssignmentsCreationStatus"`
	}
	if err := r.call(&out, "sso-admin", "list-account-assignment-creation-status", "--instance-arn", r.res.InstanceARN,
		"--filter", "Status="+statusInProgress); err != nil {
		return requestStatus{}, false, err
	}
	for _, row := range out.Rows {
		if row.RequestID == "" {
			continue
		}
		st, err := r.describeAssignment(row.RequestID)
		if err != nil {
			return requestStatus{}, false, err
		}
		if r.isOurAssignment(st) {
			if st.RequestID == "" {
				st.RequestID = row.RequestID
			}
			return st, true, nil
		}
	}
	return requestStatus{}, false, nil
}

// isOurAssignment reports whether a creation status is for this account,
// permission set and principal. All three must be set and equal ours; a status
// that leaves one out is not ours.
func (r *applyRun) isOurAssignment(st requestStatus) bool {
	return st.TargetID != "" && st.TargetID == r.res.AccountID &&
		st.PermissionSetARN != "" && st.PermissionSetARN == r.res.PermissionSetARN &&
		st.PrincipalID != "" && st.PrincipalID == r.res.PrincipalID
}

// describeAssignment reads one assignment creation status.
func (r *applyRun) describeAssignment(id string) (requestStatus, error) {
	var out struct {
		Status requestStatus `json:"AccountAssignmentCreationStatus"`
	}
	err := r.call(&out, "sso-admin", "describe-account-assignment-creation-status", "--instance-arn", r.res.InstanceARN,
		"--account-assignment-creation-request-id", id)
	return out.Status, err
}

// settleAssignment turns an assignment creation status into done or an error,
// polling while it is IN_PROGRESS.
func (r *applyRun) settleAssignment(st requestStatus) error {
	return r.settle("assignment", st, func() (requestStatus, error) {
		return r.describeAssignment(st.RequestID)
	})
}

// provision is step 7: push the set to an account whose assignment existed
// before this run.
func (r *applyRun) provision() error {
	if r.provisioned {
		return nil
	}
	var out struct {
		Status requestStatus `json:"PermissionSetProvisioningStatus"`
	}
	if err := r.call(&out, "sso-admin", "provision-permission-set", "--instance-arn", r.res.InstanceARN,
		"--permission-set-arn", r.res.PermissionSetARN, "--target-type", "AWS_ACCOUNT", "--target-id", r.res.AccountID); err != nil {
		return err
	}
	r.logf("provisioning %s to account %s (%s)", r.spec.Name, r.res.AccountID, out.Status.RequestID)
	id := out.Status.RequestID
	return r.settle("provisioning", out.Status, func() (requestStatus, error) {
		var poll struct {
			Status requestStatus `json:"PermissionSetProvisioningStatus"`
		}
		err := r.call(&poll, "sso-admin", "describe-permission-set-provisioning-status", "--instance-arn", r.res.InstanceARN,
			"--provision-permission-set-request-id", id)
		return poll.Status, err
	})
}

// settle reads a request status: SUCCEEDED is done, FAILED is the error with
// its reason, and IN_PROGRESS polls every PollInterval up to PollTimeout. A
// status that needs polling but has no RequestId is an error at once.
func (r *applyRun) settle(what string, st requestStatus, poll func() (requestStatus, error)) error {
	id := st.RequestID
	deadline := time.Now().Add(r.spec.PollTimeout)
	for {
		switch st.Status {
		case statusSucceeded:
			r.logf("%s %s succeeded", what, id)
			return nil
		case statusFailed:
			return fmt.Errorf("%s %s failed: %s", what, id, st.FailureReason)
		}
		if id == "" {
			return fmt.Errorf("%s response has status %q and no RequestId to poll", what, st.Status)
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%s request %s still %s after %s; it may still complete, rerun to check", what, id, st.Status, r.spec.PollTimeout)
		}
		if err := r.wait(); err != nil {
			return err
		}
		next, err := poll()
		if err != nil {
			return err
		}
		st = next
	}
}

// wait sleeps one PollInterval or until the context ends.
func (r *applyRun) wait() error {
	t := time.NewTimer(r.spec.PollInterval)
	defer t.Stop()
	select {
	case <-r.ctx.Done():
		return r.ctx.Err()
	case <-t.C:
		return nil
	}
}

// hint is step 8: the delegation lines, rereading the admin profile's SSO
// start URL and region. The admin profile's own sso_role_name is never
// copied; a read failure is only a warning.
func (r *applyRun) hint() error {
	r.res.Delegation = DelegationHint{AccountID: r.res.AccountID, RoleName: r.spec.Name}
	p, err := ReadSSOProfile(r.spec.AWSConfigPath, r.spec.Profile)
	if err != nil {
		if !errors.Is(err, ErrNoSSO) {
			r.logf("warning: cannot read the SSO start URL and region of profile %s: %v", r.spec.Profile, err)
		}
		return nil
	}
	r.res.Delegation.StartURL = p.StartURL
	r.res.Delegation.Region = p.Region
	return nil
}

// discoveredHint is step 8 for a profile without SSO keys: the discovered or
// given region, and --start-url else the default access portal URL of the
// identity store, verified through spec.VerifyStartURL when it is set.
func (r *applyRun) discoveredHint() error {
	url := r.spec.StartURL
	if url == "" {
		if r.res.IdentityStoreID == "" {
			return fmt.Errorf("instance %s has no identity store id to derive the start URL from; pass --start-url from the IAM Identity Center dashboard", r.res.InstanceARN)
		}
		url = "https://" + r.res.IdentityStoreID + ".awsapps.com/start"
	}
	r.res.Delegation = DelegationHint{StartURL: url, Region: r.region, AccountID: r.res.AccountID, RoleName: r.spec.Name}
	return r.verifyStartURL(url)
}

// verifyStartURL turns ErrStartURLRejected into an error and any other
// failure into a warning: the apply is done, and a transient OIDC failure
// must not fail it.
func (r *applyRun) verifyStartURL(url string) error {
	if r.spec.VerifyStartURL == nil {
		r.logf("access portal %s (not verified)", url)
		return nil
	}
	err := r.spec.VerifyStartURL(r.ctx, r.region, url)
	switch {
	case errors.Is(err, ErrStartURLRejected):
		return fmt.Errorf("OIDC rejected start URL %s; pass --start-url from the IAM Identity Center dashboard", url)
	case err != nil:
		r.logf("warning: cannot verify start URL %s (%v); keeping it", url, err)
	default:
		r.logf("access portal %s verified", url)
	}
	return nil
}

// Plan returns every aws call Apply can make, with placeholders for values an
// earlier call supplies. A run skips the calls its lookups make unnecessary.
// For a profile without SSO keys it starts with sts and discovery and ends
// with the in-process start URL verification, a step with no Args.
func Plan(spec ApplySpec) ([]ApplyStep, error) {
	spec, err := normalizeSpec(spec)
	if err != nil {
		return nil, err
	}
	p, noSSO := readProfile(spec, io.Discard)
	if !noSSO {
		steps, err := planRest(spec, p.Region)
		if err != nil {
			return nil, err
		}
		return append([]ApplyStep{
			{Name: "instance", Args: callArgs(spec.Profile, p.Region, "", "sso-admin", "list-instances")},
			{Name: "account", Args: callArgs(spec.Profile, "", "", "sts", "get-caller-identity")},
		}, steps...), nil
	}
	region := spec.SSORegion
	if region == "" {
		region = "<region>"
	}
	steps, err := planRest(spec, region)
	if err != nil {
		return nil, err
	}
	out := append(planDiscovery(spec, region), steps...)
	return append(out, ApplyStep{Name: "start URL verification: RegisterClient and StartDeviceAuthorization against oidc." + region +
		".amazonaws.com in process (no aws call)"}), nil
}

// planDiscovery is the sts call and the discovery and instance type calls of
// a profile without SSO keys. Only the aws partition gets this far, so the
// home region is us-east-1.
func planDiscovery(spec ApplySpec, region string) []ApplyStep {
	home := awsPartitionHome
	step := func(name, region string, op ...string) ApplyStep {
		return ApplyStep{Name: name, Args: callArgs(spec.Profile, region, home, op...)}
	}
	out := []ApplyStep{step("account", "", "sts", "get-caller-identity")}
	if spec.SSORegion == "" {
		out = append(out,
			step("instance in all regions", home, "sso-admin", "list-instances", "--region-scope", "ALL_REGIONS"),
			step("account regions (CLI without --region-scope)", "", "account", "list-regions", "--region-opt-status-contains", "ENABLED", "ENABLED_BY_DEFAULT"))
	}
	return append(out,
		step("instance in one region", region, "sso-admin", "list-instances"),
		step("instance type", region, "sso-admin", "describe-instance", "--instance-arn", "<instance-arn>"),
		step("organization", "", "organizations", "describe-organization"))
}

// planRest is the calls from step 3 on, in region.
func planRest(spec ApplySpec, region string) ([]ApplyStep, error) {
	const (
		inst  = "<instance-arn>"
		store = "<identity-store-id>"
		ps    = "<permission-set-arn>"
		princ = "<principal-id>"
		req   = "<request-id>"
		acct  = "<account-id>"
	)
	account := acct
	if spec.AccountID != "" {
		account = spec.AccountID
	}
	user := spec.User
	if user == "" {
		user = "<user name from the caller ARN>"
	}
	alt, err := json.Marshal(map[string]any{"UniqueAttribute": map[string]string{"AttributePath": "userName", "AttributeValue": user}})
	if err != nil {
		return nil, err
	}
	step := func(name string, op ...string) ApplyStep {
		return ApplyStep{Name: name, Args: callArgs(spec.Profile, region, "", op...)}
	}
	return []ApplyStep{
		step("find permission set", "sso-admin", "list-permission-sets", "--instance-arn", inst),
		step("find permission set", "sso-admin", "describe-permission-set", "--instance-arn", inst, "--permission-set-arn", ps),
		step("create permission set", "sso-admin", "create-permission-set", "--instance-arn", inst, "--name", spec.Name,
			"--session-duration", PermissionSetSessionDuration, "--description", permissionSetDescription),
		step("fix session duration", "sso-admin", "update-permission-set", "--instance-arn", inst, "--permission-set-arn", ps,
			"--session-duration", PermissionSetSessionDuration),
		step("inline policy", "sso-admin", "put-inline-policy-to-permission-set", "--instance-arn", inst, "--permission-set-arn", ps,
			"--inline-policy", PermissionSetPolicy(spec.Regions)),
		step("principal", "identitystore", "get-user-id", "--identity-store-id", store, "--alternate-identifier", string(alt)),
		step("find assignment", "sso-admin", "list-account-assignments", "--instance-arn", inst, "--account-id", account, "--permission-set-arn", ps),
		step("create assignment", "sso-admin", "create-account-assignment", "--instance-arn", inst, "--target-type", "AWS_ACCOUNT",
			"--target-id", account, "--permission-set-arn", ps, "--principal-type", "USER", "--principal-id", princ),
		step("in-flight assignment", "sso-admin", "list-account-assignment-creation-status", "--instance-arn", inst, "--filter", "Status="+statusInProgress),
		step("wait for assignment", "sso-admin", "describe-account-assignment-creation-status", "--instance-arn", inst,
			"--account-assignment-creation-request-id", req),
		step("provision", "sso-admin", "provision-permission-set", "--instance-arn", inst, "--permission-set-arn", ps,
			"--target-type", "AWS_ACCOUNT", "--target-id", account),
		step("wait for provisioning", "sso-admin", "describe-permission-set-provisioning-status", "--instance-arn", inst,
			"--provision-permission-set-request-id", req),
	}, nil
}
