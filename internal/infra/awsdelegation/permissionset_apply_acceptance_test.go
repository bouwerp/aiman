//go:build acceptance

package awsdelegation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scriptedAWS is a fake AWSCLI. It records every call and answers from handle,
// which sees the full argument list of one aws invocation.
type scriptedAWS struct {
	calls  [][]string
	handle func(args []string) (string, error)
}

func (s *scriptedAWS) Run(_ context.Context, args ...string) ([]byte, error) {
	s.calls = append(s.calls, append([]string(nil), args...))
	out, err := s.handle(args)
	return []byte(out), err
}

// op returns "<service> <subcommand>" of an aws argument list, skipping global
// flags such as --profile, --region and --output that may precede them.
func op(args []string) string {
	var words []string
	for i := 0; i < len(args) && len(words) < 2; i++ {
		if strings.HasPrefix(args[i], "--") {
			i++ // skip the flag's value
			continue
		}
		words = append(words, args[i])
	}
	return strings.Join(words, " ")
}

func flagValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func (s *scriptedAWS) callsOf(name string) [][]string {
	var out [][]string
	for _, c := range s.calls {
		if op(c) == name {
			out = append(out, c)
		}
	}
	return out
}

// world is the Identity Center state a scripted fake answers from.
type world struct {
	instances        []map[string]string
	callerARN        string
	callerAccount    string
	permissionSets   map[string]map[string]string // arn -> describe fields
	users            map[string]string            // user name -> id
	assignments      []map[string]string
	createStatus     string   // status create-account-assignment answers; empty: IN_PROGRESS
	createConflict   bool     // create-account-assignment answers ConflictException
	statuses         []string // successive describe-account-assignment-creation-status answers
	failureReason    string
	statusCalls      int
	provisionStatus  []string // successive describe-permission-set-provisioning-status answers
	provisionCalls   int
	provisionFailure string
}

func defaultWorld() *world {
	return &world{
		instances:     []map[string]string{{"InstanceArn": "arn:aws:sso:::instance/ssoins-1", "IdentityStoreId": "d-123"}},
		callerARN:     "arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_AdministratorAccess_abc123/pieter@acme.io",
		callerAccount: "123456789012",
		permissionSets: map[string]map[string]string{
			"arn:aws:sso:::permissionSet/ssoins-1/ps-other": {"Name": "Other", "SessionDuration": "PT1H"},
		},
		users:           map[string]string{"pieter@acme.io": "u-1"},
		statuses:        []string{"IN_PROGRESS", "SUCCEEDED"},
		provisionStatus: []string{"IN_PROGRESS", "SUCCEEDED"},
	}
}

func (w *world) handle(args []string) (string, error) {
	j := func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	}
	switch op(args) {
	case "sso-admin list-instances":
		return j(map[string]any{"Instances": w.instances})
	case "sts get-caller-identity":
		return j(map[string]string{"Account": w.callerAccount, "Arn": w.callerARN, "UserId": "AROA:pieter@acme.io"})
	case "sso-admin list-permission-sets":
		// The CLI auto-paginates, so one call returns every ARN.
		var arns []string
		for arn := range w.permissionSets {
			arns = append(arns, arn)
		}
		return j(map[string]any{"PermissionSets": arns})
	case "sso-admin describe-permission-set":
		arn := flagValue(args, "--permission-set-arn")
		ps, ok := w.permissionSets[arn]
		if !ok {
			return "", errors.New("An error occurred (ResourceNotFoundException) when calling the DescribePermissionSet operation")
		}
		fields := map[string]string{"PermissionSetArn": arn}
		for k, v := range ps {
			fields[k] = v
		}
		return j(map[string]any{"PermissionSet": fields})
	case "sso-admin create-permission-set":
		arn := "arn:aws:sso:::permissionSet/ssoins-1/ps-new"
		fields := map[string]string{"PermissionSetArn": arn, "Name": flagValue(args, "--name"), "SessionDuration": flagValue(args, "--session-duration")}
		w.permissionSets[arn] = map[string]string{"Name": fields["Name"], "SessionDuration": fields["SessionDuration"]}
		return j(map[string]any{"PermissionSet": fields})
	case "sso-admin update-permission-set", "sso-admin put-inline-policy-to-permission-set":
		return "", nil
	case "identitystore get-user-id":
		var alt struct {
			UniqueAttribute struct {
				AttributePath  string `json:"AttributePath"`
				AttributeValue string `json:"AttributeValue"`
			} `json:"UniqueAttribute"`
		}
		if err := json.Unmarshal([]byte(flagValue(args, "--alternate-identifier")), &alt); err != nil || alt.UniqueAttribute.AttributePath != "userName" {
			return "", fmt.Errorf("ValidationException: bad --alternate-identifier %q", flagValue(args, "--alternate-identifier"))
		}
		id, ok := w.users[alt.UniqueAttribute.AttributeValue]
		if !ok {
			return "", errors.New("An error occurred (ResourceNotFoundException) when calling the GetUserId operation: USER not found.")
		}
		return j(map[string]string{"UserId": id, "IdentityStoreId": flagValue(args, "--identity-store-id")})
	case "sso-admin list-account-assignments":
		return j(map[string]any{"AccountAssignments": w.assignments})
	case "sso-admin create-account-assignment":
		if w.createConflict {
			return "", errors.New("An error occurred (ConflictException) when calling the CreateAccountAssignment operation: Assignment already exists")
		}
		st := w.createStatus
		if st == "" {
			st = "IN_PROGRESS"
		}
		return j(map[string]any{"AccountAssignmentCreationStatus": map[string]string{"RequestId": "req-1", "Status": st}})
	case "sso-admin describe-account-assignment-creation-status":
		st := w.statuses[len(w.statuses)-1]
		if w.statusCalls < len(w.statuses) {
			st = w.statuses[w.statusCalls]
		}
		w.statusCalls++
		fields := map[string]string{"RequestId": flagValue(args, "--account-assignment-creation-request-id"), "Status": st}
		if st == "FAILED" {
			fields["FailureReason"] = w.failureReason
		}
		return j(map[string]any{"AccountAssignmentCreationStatus": fields})
	case "sso-admin provision-permission-set":
		return j(map[string]any{"PermissionSetProvisioningStatus": map[string]string{"RequestId": "prov-1", "Status": "IN_PROGRESS"}})
	case "sso-admin describe-permission-set-provisioning-status":
		st := w.provisionStatus[len(w.provisionStatus)-1]
		if w.provisionCalls < len(w.provisionStatus) {
			st = w.provisionStatus[w.provisionCalls]
		}
		w.provisionCalls++
		fields := map[string]string{"RequestId": flagValue(args, "--provision-permission-set-request-id"), "Status": st}
		if st == "FAILED" {
			fields["FailureReason"] = w.provisionFailure
		}
		return j(map[string]any{"PermissionSetProvisioningStatus": fields})
	}
	return "", fmt.Errorf("unexpected aws call: %v", args)
}

func writeAWSConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const adminProfileSection = "[profile acme-admin]\nregion = us-east-1\nsso_start_url = https://d-123.awsapps.com/start\nsso_region = eu-west-1\nsso_account_id = 123456789012\nsso_role_name = AdministratorAccess\n"

func fastSpec(t *testing.T) ApplySpec {
	t.Helper()
	return ApplySpec{
		Profile:       "acme-admin",
		Regions:       []string{"eu-west-1"},
		AWSConfigPath: writeAWSConfig(t, adminProfileSection),
		PollInterval:  time.Millisecond,
		PollTimeout:   200 * time.Millisecond,
	}
}

// withExisting puts a complete aiman-delegated setup into the world.
func withExisting(w *world) string {
	existing := "arn:aws:sso:::permissionSet/ssoins-1/ps-aiman"
	w.permissionSets[existing] = map[string]string{"Name": DefaultPermissionSetName, "SessionDuration": PermissionSetSessionDuration}
	w.assignments = []map[string]string{{"PrincipalId": "u-1", "PrincipalType": "USER", "PermissionSetArn": existing, "AccountId": "123456789012"}}
	return existing
}

func TestApplyCreatesEverythingWhenMissing(t *testing.T) {
	w := defaultWorld()
	cli := &scriptedAWS{handle: w.handle}
	var log strings.Builder
	res, err := Apply(context.Background(), cli, fastSpec(t), &log)
	if err != nil {
		t.Fatalf("Apply: %v\nlog:\n%s", err, log.String())
	}
	if res.InstanceARN != "arn:aws:sso:::instance/ssoins-1" || res.IdentityStoreID != "d-123" {
		t.Errorf("instance = %+v", res)
	}
	if res.AccountID != "123456789012" {
		t.Errorf("AccountID = %q, want the caller's account", res.AccountID)
	}
	if !res.CreatedPermissionSet || res.PermissionSetARN != "arn:aws:sso:::permissionSet/ssoins-1/ps-new" {
		t.Errorf("permission set: created=%v arn=%q", res.CreatedPermissionSet, res.PermissionSetARN)
	}
	if res.UserName != "pieter@acme.io" || res.PrincipalID != "u-1" {
		t.Errorf("principal = %q/%q", res.UserName, res.PrincipalID)
	}
	if !res.CreatedAssignment {
		t.Error("CreatedAssignment = false, want true")
	}
	want := DelegationHint{StartURL: "https://d-123.awsapps.com/start", Region: "eu-west-1", AccountID: "123456789012", RoleName: DefaultPermissionSetName}
	if res.Delegation != want {
		t.Errorf("Delegation = %+v, want %+v", res.Delegation, want)
	}

	creates := cli.callsOf("sso-admin create-permission-set")
	if len(creates) != 1 {
		t.Fatalf("create-permission-set calls = %d, want 1", len(creates))
	}
	if flagValue(creates[0], "--name") != DefaultPermissionSetName || flagValue(creates[0], "--session-duration") != PermissionSetSessionDuration || flagValue(creates[0], "--instance-arn") != res.InstanceARN {
		t.Errorf("create-permission-set args = %v", creates[0])
	}
	puts := cli.callsOf("sso-admin put-inline-policy-to-permission-set")
	if len(puts) != 1 {
		t.Fatalf("put-inline-policy calls = %d, want 1", len(puts))
	}
	var a, b any
	if json.Unmarshal([]byte(flagValue(puts[0], "--inline-policy")), &a) != nil || json.Unmarshal([]byte(PermissionSetPolicy([]string{"eu-west-1"})), &b) != nil || fmt.Sprint(a) != fmt.Sprint(b) {
		t.Errorf("inline policy differs from PermissionSetPolicy:\n%s", flagValue(puts[0], "--inline-policy"))
	}
	if flagValue(puts[0], "--permission-set-arn") != res.PermissionSetARN || flagValue(puts[0], "--instance-arn") != res.InstanceARN {
		t.Errorf("put-inline-policy lacks the arns: %v", puts[0])
	}
	assigns := cli.callsOf("sso-admin create-account-assignment")
	if len(assigns) != 1 {
		t.Fatalf("create-account-assignment calls = %d, want 1", len(assigns))
	}
	for flag, want := range map[string]string{
		"--instance-arn":       res.InstanceARN,
		"--target-type":        "AWS_ACCOUNT",
		"--target-id":          "123456789012",
		"--permission-set-arn": res.PermissionSetARN,
		"--principal-type":     "USER",
		"--principal-id":       "u-1",
	} {
		if got := flagValue(assigns[0], flag); got != want {
			t.Errorf("create-account-assignment %s = %q, want %q", flag, got, want)
		}
	}
	polls := cli.callsOf("sso-admin describe-account-assignment-creation-status")
	if len(polls) != 2 {
		t.Fatalf("status polls = %d, want 2 (IN_PROGRESS then SUCCEEDED)", len(polls))
	}
	if flagValue(polls[0], "--account-assignment-creation-request-id") != "req-1" || flagValue(polls[0], "--instance-arn") != res.InstanceARN {
		t.Errorf("status poll args = %v", polls[0])
	}
	if n := len(cli.callsOf("sso-admin provision-permission-set")); n != 0 {
		t.Errorf("provision-permission-set called %d times after a fresh assignment, which already provisions", n)
	}
	lists := cli.callsOf("sso-admin list-account-assignments")
	if len(lists) != 1 || flagValue(lists[0], "--account-id") != "123456789012" || flagValue(lists[0], "--permission-set-arn") != res.PermissionSetARN || flagValue(lists[0], "--instance-arn") != res.InstanceARN {
		t.Errorf("list-account-assignments calls = %v", lists)
	}
	for _, c := range cli.calls {
		if flagValue(c, "--profile") != "acme-admin" {
			t.Errorf("call without --profile acme-admin: %v", c)
		}
		switch strings.Fields(op(c))[0] {
		case "sso-admin", "identitystore":
			if flagValue(c, "--region") != "eu-west-1" {
				t.Errorf("Identity Center call without --region eu-west-1 (the profile's sso_region): %v", c)
			}
		}
		if flagValue(c, "--next-token") != "" || flagValue(c, "--starting-token") != "" {
			t.Errorf("manual paging flag on %v; the CLI paginates on its own", c)
		}
	}
	if !strings.Contains(log.String(), "123456789012") {
		t.Errorf("log does not name the account:\n%s", log.String())
	}
	if strings.Contains(log.String(), "AROA:") || strings.Contains(log.String(), "UserId") {
		t.Errorf("log carries identity material it should not:\n%s", log.String())
	}
}

func TestApplyIsIdempotentAndProvisions(t *testing.T) {
	w := defaultWorld()
	existing := withExisting(w)
	cli := &scriptedAWS{handle: w.handle}
	res, err := Apply(context.Background(), cli, fastSpec(t), &strings.Builder{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.CreatedPermissionSet || res.CreatedAssignment {
		t.Errorf("created flags = %v/%v, want false/false", res.CreatedPermissionSet, res.CreatedAssignment)
	}
	if res.PermissionSetARN != existing {
		t.Errorf("PermissionSetARN = %q, want the existing one", res.PermissionSetARN)
	}
	for _, name := range []string{"sso-admin create-permission-set", "sso-admin update-permission-set", "sso-admin create-account-assignment", "sso-admin describe-account-assignment-creation-status"} {
		if n := len(cli.callsOf(name)); n != 0 {
			t.Errorf("%s called %d times on a complete setup", name, n)
		}
	}
	if n := len(cli.callsOf("sso-admin put-inline-policy-to-permission-set")); n != 1 {
		t.Errorf("put-inline-policy calls = %d, want 1 (the policy is always re-applied)", n)
	}
	provs := cli.callsOf("sso-admin provision-permission-set")
	if len(provs) != 1 {
		t.Fatalf("provision-permission-set calls = %d, want 1 so the live role picks up the policy", len(provs))
	}
	for flag, want := range map[string]string{"--instance-arn": res.InstanceARN, "--permission-set-arn": existing, "--target-type": "AWS_ACCOUNT", "--target-id": "123456789012"} {
		if got := flagValue(provs[0], flag); got != want {
			t.Errorf("provision-permission-set %s = %q, want %q", flag, got, want)
		}
	}
	polls := cli.callsOf("sso-admin describe-permission-set-provisioning-status")
	if len(polls) != 2 || flagValue(polls[0], "--provision-permission-set-request-id") != "prov-1" {
		t.Errorf("provisioning polls = %v, want 2 with the request id", polls)
	}
}

func TestApplyProvisioningFailed(t *testing.T) {
	w := defaultWorld()
	withExisting(w)
	w.provisionStatus = []string{"FAILED"}
	w.provisionFailure = "Permission set provisioning failed in account 123456789012"
	cli := &scriptedAWS{handle: w.handle}
	_, err := Apply(context.Background(), cli, fastSpec(t), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), w.provisionFailure) {
		t.Fatalf("err = %v, want one carrying the provisioning FailureReason", err)
	}
}

func TestApplyFixesSessionDuration(t *testing.T) {
	w := defaultWorld()
	existing := withExisting(w)
	w.permissionSets[existing]["SessionDuration"] = "PT1H"
	cli := &scriptedAWS{handle: w.handle}
	if _, err := Apply(context.Background(), cli, fastSpec(t), &strings.Builder{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ups := cli.callsOf("sso-admin update-permission-set")
	if len(ups) != 1 || flagValue(ups[0], "--session-duration") != PermissionSetSessionDuration || flagValue(ups[0], "--permission-set-arn") != existing || flagValue(ups[0], "--instance-arn") == "" {
		t.Errorf("update-permission-set calls = %v", ups)
	}
	if n := len(cli.callsOf("sso-admin create-permission-set")); n != 0 {
		t.Errorf("create-permission-set called %d times for an existing set", n)
	}
	if n := len(cli.callsOf("sso-admin provision-permission-set")); n != 1 {
		t.Errorf("provision-permission-set calls = %d, want 1 after a duration change on an assigned set", n)
	}
}

func TestApplyAssignmentConflictIsPresent(t *testing.T) {
	w := defaultWorld()
	existing := withExisting(w)
	// The listing lags: the assignment exists but is not listed yet.
	w.assignments = nil
	w.createConflict = true
	calls := 0
	cli := &scriptedAWS{}
	cli.handle = func(args []string) (string, error) {
		if op(args) == "sso-admin list-account-assignments" {
			calls++
			if calls > 1 {
				w.assignments = []map[string]string{{"PrincipalId": "u-1", "PrincipalType": "USER", "PermissionSetArn": existing, "AccountId": "123456789012"}}
			}
		}
		return w.handle(args)
	}
	res, err := Apply(context.Background(), cli, fastSpec(t), &strings.Builder{})
	if err != nil {
		t.Fatalf("Apply: %v (ConflictException on create means the assignment is there)", err)
	}
	if res.CreatedAssignment {
		t.Error("CreatedAssignment = true after a ConflictException")
	}
	if n := len(cli.callsOf("sso-admin list-account-assignments")); n < 2 {
		t.Errorf("list-account-assignments calls = %d, want a second look after the conflict", n)
	}
}

func TestApplySkipsPollWhenCreateSucceeds(t *testing.T) {
	w := defaultWorld()
	w.createStatus = "SUCCEEDED"
	cli := &scriptedAWS{handle: w.handle}
	res, err := Apply(context.Background(), cli, fastSpec(t), &strings.Builder{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.CreatedAssignment {
		t.Error("CreatedAssignment = false")
	}
	if n := len(cli.callsOf("sso-admin describe-account-assignment-creation-status")); n != 0 {
		t.Errorf("status polls = %d after an immediate SUCCEEDED, want 0", n)
	}
}

func TestApplyRefusesZeroOrManyInstances(t *testing.T) {
	for name, instances := range map[string][]map[string]string{
		"none": nil,
		"two": {
			{"InstanceArn": "arn:aws:sso:::instance/ssoins-1", "IdentityStoreId": "d-1"},
			{"InstanceArn": "arn:aws:sso:::instance/ssoins-2", "IdentityStoreId": "d-2"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := defaultWorld()
			w.instances = instances
			cli := &scriptedAWS{handle: w.handle}
			_, err := Apply(context.Background(), cli, fastSpec(t), &strings.Builder{})
			if err == nil {
				t.Fatal("Apply succeeded, want an error")
			}
			if name == "none" && (!strings.Contains(err.Error(), "acme-admin") || !strings.Contains(err.Error(), "eu-west-1")) {
				t.Errorf("error does not name the profile and the Identity Center region: %v", err)
			}
			if name == "two" && (!strings.Contains(err.Error(), "ssoins-1") || !strings.Contains(err.Error(), "ssoins-2")) {
				t.Errorf("error does not list both instances: %v", err)
			}
			if len(cli.calls) != 1 {
				t.Errorf("calls after the instance check = %d, want none", len(cli.calls)-1)
			}
		})
	}
}

func TestApplyWithoutSSORegionUsesProfileDefault(t *testing.T) {
	w := defaultWorld()
	cli := &scriptedAWS{handle: w.handle}
	spec := fastSpec(t)
	spec.AWSConfigPath = writeAWSConfig(t, "[profile acme-admin]\nregion = eu-west-1\n")
	res, err := Apply(context.Background(), cli, spec, &strings.Builder{})
	if err != nil {
		t.Fatalf("Apply: %v (a non-SSO admin profile must not fail the apply)", err)
	}
	for _, c := range cli.calls {
		if flagValue(c, "--region") != "" {
			t.Errorf("--region passed with no sso_region to take it from: %v", c)
		}
	}
	if res.Delegation.StartURL != "" || res.Delegation.Region != "" || res.Delegation.AccountID != "123456789012" || res.Delegation.RoleName != DefaultPermissionSetName {
		t.Errorf("Delegation = %+v", res.Delegation)
	}
}

func TestApplyRequiresRegionsBeforeAnyCall(t *testing.T) {
	cli := &scriptedAWS{handle: defaultWorld().handle}
	spec := fastSpec(t)
	spec.Regions = nil
	_, err := Apply(context.Background(), cli, spec, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "--regions") {
		t.Fatalf("err = %v, want one naming --regions", err)
	}
	if len(cli.calls) != 0 {
		t.Errorf("aws was called %d times without regions", len(cli.calls))
	}
}

func TestApplyResolvesUser(t *testing.T) {
	cases := []struct {
		name, arn, user, want, wantErr string
	}{
		{"sso role session", "arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Admin_abc/pieter@acme.io", "", "pieter@acme.io", ""},
		{"explicit user wins", "arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Admin_abc/someone-else", "aiman-phone", "aiman-phone", ""},
		{"iam user needs --user", "arn:aws:iam::123456789012:user/pieter", "", "", "--user"},
		{"other role needs --user", "arn:aws:sts::123456789012:assumed-role/Deploy/botocore-session-1700000000", "", "", "--user"},
		{"root needs --user", "arn:aws:iam::123456789012:root", "", "", "--user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := defaultWorld()
			w.callerARN = tc.arn
			w.users = map[string]string{"pieter@acme.io": "u-1", "aiman-phone": "u-3"}
			cli := &scriptedAWS{handle: w.handle}
			spec := fastSpec(t)
			spec.User = tc.user
			res, err := Apply(context.Background(), cli, spec, &strings.Builder{})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one mentioning %q", err, tc.wantErr)
				}
				if n := len(cli.callsOf("identitystore get-user-id")); n != 0 {
					t.Errorf("get-user-id called %d times with no trustworthy user name", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if res.UserName != tc.want {
				t.Errorf("UserName = %q, want %q", res.UserName, tc.want)
			}
			gu := cli.callsOf("identitystore get-user-id")
			if len(gu) != 1 || flagValue(gu[0], "--identity-store-id") != "d-123" {
				t.Fatalf("get-user-id calls = %v", gu)
			}
			alt := flagValue(gu[0], "--alternate-identifier")
			if !strings.Contains(alt, `"userName"`) || !strings.Contains(alt, tc.want) {
				t.Errorf("--alternate-identifier = %s, want UniqueAttribute userName = %s", alt, tc.want)
			}
			if n := len(cli.callsOf("identitystore list-users")); n != 0 {
				t.Errorf("list-users called %d times; get-user-id is the lookup", n)
			}
		})
	}
}

func TestApplyUnknownUser(t *testing.T) {
	w := defaultWorld()
	w.users = map[string]string{}
	cli := &scriptedAWS{handle: w.handle}
	_, err := Apply(context.Background(), cli, fastSpec(t), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "pieter@acme.io") || !strings.Contains(err.Error(), "d-123") || !strings.Contains(err.Error(), "--user") {
		t.Fatalf("err = %v, want one naming the user, the identity store and --user", err)
	}
	if n := len(cli.callsOf("sso-admin create-account-assignment")); n != 0 {
		t.Errorf("create-account-assignment called %d times without a principal", n)
	}
}

func TestApplyAssignmentFailed(t *testing.T) {
	w := defaultWorld()
	w.statuses = []string{"IN_PROGRESS", "FAILED"}
	w.failureReason = "Received a 404 status error: Permission set provisioning failed"
	cli := &scriptedAWS{handle: w.handle}
	_, err := Apply(context.Background(), cli, fastSpec(t), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), w.failureReason) {
		t.Fatalf("err = %v, want one carrying the FailureReason", err)
	}
}

func TestApplyAssignmentTimeoutNamesRequest(t *testing.T) {
	w := defaultWorld()
	w.statuses = []string{"IN_PROGRESS"}
	cli := &scriptedAWS{handle: w.handle}
	spec := fastSpec(t)
	spec.PollTimeout = 20 * time.Millisecond
	_, err := Apply(context.Background(), cli, spec, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "req-1") {
		t.Fatalf("err = %v, want one naming request req-1", err)
	}
	if n := len(cli.callsOf("sso-admin describe-account-assignment-creation-status")); n < 2 {
		t.Errorf("status polls = %d, want repeated polling until the timeout", n)
	}
}

func TestApplyHintFailureIsAWarning(t *testing.T) {
	w := defaultWorld()
	cli := &scriptedAWS{handle: w.handle}
	spec := fastSpec(t)
	// The config is read twice: for the region up front and for the hint at
	// the end. Removing it between the two must not fail a finished apply.
	cli.handle = func(args []string) (string, error) {
		if op(args) == "sso-admin create-account-assignment" {
			_ = os.Remove(spec.AWSConfigPath)
		}
		return w.handle(args)
	}
	var log strings.Builder
	res, err := Apply(context.Background(), cli, spec, &log)
	if err != nil {
		t.Fatalf("Apply: %v, want success with a warning in the log", err)
	}
	if res.Delegation.AccountID != "123456789012" || res.Delegation.RoleName != DefaultPermissionSetName {
		t.Errorf("Delegation = %+v, want account and role at least", res.Delegation)
	}
	if !strings.Contains(strings.ToLower(log.String()), "warn") {
		t.Errorf("log has no warning about the unreadable hint:\n%s", log.String())
	}
}

func TestApplyCustomNameAndAccount(t *testing.T) {
	w := defaultWorld()
	cli := &scriptedAWS{handle: w.handle}
	spec := fastSpec(t)
	spec.Name = "team-delegated"
	spec.AccountID = "999999999999"
	res, err := Apply(context.Background(), cli, spec, &strings.Builder{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.AccountID != "999999999999" || res.Delegation.AccountID != "999999999999" || res.Delegation.RoleName != "team-delegated" {
		t.Errorf("result = %+v", res)
	}
	creates := cli.callsOf("sso-admin create-permission-set")
	if len(creates) != 1 || flagValue(creates[0], "--name") != "team-delegated" {
		t.Errorf("create-permission-set calls = %v", creates)
	}
	if n := len(cli.callsOf("sts get-caller-identity")); n != 1 {
		t.Errorf("get-caller-identity calls = %d, want 1 (still needed for the user name)", n)
	}
}

func TestApplyWrapsCLIErrors(t *testing.T) {
	w := defaultWorld()
	cli := &scriptedAWS{handle: func(args []string) (string, error) {
		if op(args) == "sso-admin put-inline-policy-to-permission-set" {
			return "", errors.New("An error occurred (AccessDeniedException) when calling the PutInlinePolicyToPermissionSet operation: not authorized\nsecond line")
		}
		return w.handle(args)
	}}
	_, err := Apply(context.Background(), cli, fastSpec(t), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "AccessDeniedException") || !strings.Contains(err.Error(), "put-inline-policy-to-permission-set") {
		t.Fatalf("err = %v, want the subcommand and the CLI's message", err)
	}
	if strings.Contains(err.Error(), "second line") {
		t.Errorf("err = %v, want only the first line of the CLI output", err)
	}
}

func TestPlanListsCallsWithPlaceholders(t *testing.T) {
	spec := fastSpec(t)
	steps, err := Plan(spec)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	joined := ""
	for _, s := range steps {
		if s.Name == "" || len(s.Args) == 0 {
			t.Errorf("step without name or args: %+v", s)
		}
		joined += op(s.Args) + "\n" + strings.Join(s.Args, " ") + "\n"
	}
	for _, want := range []string{
		"sso-admin list-instances",
		"sts get-caller-identity",
		"sso-admin list-permission-sets",
		"sso-admin describe-permission-set",
		"sso-admin create-permission-set",
		"sso-admin put-inline-policy-to-permission-set",
		"identitystore get-user-id",
		"sso-admin list-account-assignments",
		"sso-admin create-account-assignment",
		"sso-admin describe-account-assignment-creation-status",
		"sso-admin provision-permission-set",
		"sso-admin describe-permission-set-provisioning-status",
		"<instance-arn>", "<identity-store-id>", "<permission-set-arn>", "<principal-id>", "<request-id>",
		"--session-duration " + PermissionSetSessionDuration,
		"--region eu-west-1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("plan lacks %q:\n%s", want, joined)
		}
	}
	for _, s := range steps {
		if flagValue(s.Args, "--profile") != "acme-admin" {
			t.Errorf("planned step without --profile: %v", s.Args)
		}
	}
	spec.Regions = nil
	if _, err := Plan(spec); err == nil {
		t.Error("Plan without regions succeeded, want an error")
	}
}

func TestExecAWSCLIBuildsArgs(t *testing.T) {
	cli := ExecAWSCLI{Profile: "acme-admin"}
	got := cli.command(context.Background(), "--region", "eu-west-1", "sso-admin", "list-instances").Args
	want := []string{"aws", "--profile", "acme-admin", "--output", "json", "--region", "eu-west-1", "sso-admin", "list-instances"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", got, want)
	}
}
