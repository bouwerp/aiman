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
// else err itself.
func runError(args []string, stdout, stderr string, err error) error {
	msg := firstLine(stderr)
	if msg == "" {
		msg = firstLine(stdout)
	}
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("aws %s: %s", awsOperation(args), msg)
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
}

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

// ssoRegion is the admin profile's sso_region, or empty when the profile has
// no SSO keys or the config cannot be read. Any failure other than a profile
// without SSO keys is a warning line on log, so an unreadable config is not
// mistaken for a non-SSO profile.
func ssoRegion(spec ApplySpec, log io.Writer) string {
	if spec.AWSConfigPath == "" {
		_, _ = fmt.Fprintf(log, "warning: no home directory to find ~/.aws/config; Identity Center calls use the profile's default region\n")
		return ""
	}
	p, err := ReadSSOProfile(spec.AWSConfigPath, spec.Profile)
	if err != nil {
		if !errors.Is(err, ErrNoSSO) {
			_, _ = fmt.Fprintf(log, "warning: cannot read sso_region of profile %s: %v; Identity Center calls use the profile's default region\n", spec.Profile, err)
		}
		return ""
	}
	return p.Region
}

// Apply creates or updates the delegated permission set, puts its inline
// policy, assigns it to the user for the account and provisions it when the
// assignment already existed. Each step logs one line to log.
func Apply(ctx context.Context, cli AWSCLI, spec ApplySpec, log io.Writer) (ApplyResult, error) {
	spec, err := normalizeSpec(spec)
	if err != nil {
		return ApplyResult{}, err
	}
	r := &applyRun{ctx: ctx, cli: cli, spec: spec, log: log, region: ssoRegion(spec, log)}
	for _, step := range []func() error{
		r.instance,
		r.account,
		r.permissionSet,
		r.policy,
		r.principal,
		r.assignment,
		r.provision,
	} {
		if err := step(); err != nil {
			return r.res, err
		}
	}
	r.hint()
	return r.res, nil
}

// logf writes one progress line.
func (r *applyRun) logf(format string, a ...any) {
	_, _ = fmt.Fprintf(r.log, format+"\n", a...)
}

// args builds an aws argument list: --profile, --region for Identity Center
// calls when the admin profile has an sso_region, then the operation.
func (r *applyRun) args(op ...string) []string {
	return callArgs(r.spec.Profile, r.region, op...)
}

func callArgs(profile, region string, op ...string) []string {
	out := []string{"--profile", profile}
	if region != "" && len(op) > 0 && (op[0] == "sso-admin" || op[0] == "identitystore") {
		out = append(out, "--region", region)
	}
	return append(out, op...)
}

// call runs one aws invocation and decodes its JSON into v when v is not nil.
func (r *applyRun) call(v any, op ...string) error {
	args := r.args(op...)
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

// instance is step 1: exactly one Identity Center instance.
func (r *applyRun) instance() error {
	var out struct {
		Instances []struct {
			InstanceArn     string `json:"InstanceArn"`
			IdentityStoreID string `json:"IdentityStoreId"`
		} `json:"Instances"`
	}
	if err := r.call(&out, "sso-admin", "list-instances"); err != nil {
		return err
	}
	switch len(out.Instances) {
	case 0:
		region := r.region
		if region == "" {
			region = "the profile's default region"
		}
		return fmt.Errorf("no Identity Center instance visible to profile %s in %s; check the profile's sso_region", r.spec.Profile, region)
	case 1:
	default:
		arns := make([]string, 0, len(out.Instances))
		for _, in := range out.Instances {
			arns = append(arns, in.InstanceArn)
		}
		return fmt.Errorf("profile %s sees %d Identity Center instances (%s); expected one", r.spec.Profile, len(arns), strings.Join(arns, ", "))
	}
	r.res.InstanceARN = out.Instances[0].InstanceArn
	r.res.IdentityStoreID = out.Instances[0].IdentityStoreID
	r.logf("instance %s (identity store %s)", r.res.InstanceARN, r.res.IdentityStoreID)
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

// principal is step 5: the Identity Center user id of spec.User or the caller.
func (r *applyRun) principal() error {
	user := strings.TrimSpace(r.spec.User)
	if user == "" {
		u, ok := ssoSessionUser(r.callerARN)
		if !ok {
			return fmt.Errorf("cannot infer your Identity Center user name from %s; pass --user <Identity Center user name>", r.callerARN)
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
			return fmt.Errorf("no user %s in identity store %s; pass --user with the Identity Center user name", user, r.res.IdentityStoreID)
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

// hint is step 8: the delegation lines. The admin profile's own sso_role_name
// is never copied; a read failure is only a warning.
func (r *applyRun) hint() {
	r.res.Delegation = DelegationHint{AccountID: r.res.AccountID, RoleName: r.spec.Name}
	p, err := ReadSSOProfile(r.spec.AWSConfigPath, r.spec.Profile)
	if err != nil {
		if !errors.Is(err, ErrNoSSO) {
			r.logf("warning: cannot read the SSO start URL and region of profile %s: %v", r.spec.Profile, err)
		}
		return
	}
	r.res.Delegation.StartURL = p.StartURL
	r.res.Delegation.Region = p.Region
}

// Plan returns every aws call Apply can make, with placeholders for values an
// earlier call supplies. A run skips the calls its lookups make unnecessary.
func Plan(spec ApplySpec) ([]ApplyStep, error) {
	spec, err := normalizeSpec(spec)
	if err != nil {
		return nil, err
	}
	region := ssoRegion(spec, io.Discard)
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
		return ApplyStep{Name: name, Args: callArgs(spec.Profile, region, op...)}
	}
	return []ApplyStep{
		step("instance", "sso-admin", "list-instances"),
		step("account", "sts", "get-caller-identity"),
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
