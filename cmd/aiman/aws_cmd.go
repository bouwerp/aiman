package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/shellx"
	"gopkg.in/yaml.v3"
)

const awsUsage = "Usage: aiman aws permission-set-policy [--regions a,b] [--name NAME] [--account ID] [--steps]\n" +
	"       aiman aws permission-set-apply --profile ADMIN_PROFILE [--account ID] [--regions a,b] [--name NAME] [--user USER]\n" +
	"         [--remote NAME [--delegation-profile NAME]] [--dry-run] [--json]\n" +
	"       aiman aws log [--since SEQ] [--limit N] [--json]\n" +
	"\n" +
	"permission-set-apply creates the delegated permission set in IAM Identity Center with the\n" +
	"AWS CLI v2, puts its region-locked policy and assigns it for the account. --user names the\n" +
	"Identity Center user to assign, such as a dedicated sign-in user that holds only\n" +
	"aiman-delegated; without it the admin's own user is assigned when the admin profile is an\n" +
	"Identity Center session. --remote writes the aws_delegations entry (named by\n" +
	"--delegation-profile, else the admin profile) into ~/.aiman/config.yaml after a successful\n" +
	"apply. --dry-run prints the aws calls and makes none.\n"

// newAWSCLI builds the aws runner for permission-set-apply; tests replace it.
var newAWSCLI = func(profile string) awsdelegation.AWSCLI { return awsdelegation.ExecAWSCLI{Profile: profile} }

const awsLogUsage = "Usage: aiman aws log [--since SEQ] [--limit N] [--json]\n"

// awsLogCaller is the aws.log round trip; tests replace it.
type awsLogCaller func(params map[string]any) (server.Response, error)

// runAWSLog prints serve's AWS sign-in ledger, one formatAWSLogLine per row,
// or the raw result with --json.
func runAWSLog(args []string, call awsLogCaller, out io.Writer) error {
	fs := flag.NewFlagSet("aws log", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	since := fs.Int64("since", 0, "rows after this ledger seq")
	limit := fs.Int("limit", 0, "most rows to return")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprint(os.Stderr, awsLogUsage)
		return errUsage
	}
	params := map[string]any{}
	if *since > 0 {
		params["since_seq"] = *since
	}
	if *limit > 0 {
		params["limit"] = *limit
	}
	resp, err := call(params)
	if err != nil {
		code := server.CodeServerNotRunning
		if !errors.Is(err, server.ErrServerNotRunning) {
			code = server.CodeInvalidParams
		}
		writeCLIError(code, err.Error())
		return err
	}
	if resp.Error != nil {
		writeCLIError(resp.Error.Code, resp.Error.Message)
		return errors.New(resp.Error.Message)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return err
	}
	if *asJSON {
		_, err := fmt.Fprintln(out, string(raw))
		return err
	}
	var result struct {
		Events []domain.SessionEvent `json:"events"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	for _, ev := range result.Events {
		if _, err := fmt.Fprintln(out, formatAWSLogLine(ev)); err != nil {
			return err
		}
	}
	return nil
}

// callAWSLog is runAWSLog's caller against this host's serve socket.
func callAWSLog(params map[string]any) (server.Response, error) {
	sock, err := socketPath()
	if err != nil {
		return server.Response{}, err
	}
	return server.Call(sock, "aws.log", params)
}

// formatAWSLogLine renders one sign-in ledger row as
// "<at RFC3339> <kind> profile=<p> state=<s>", plus " failure=<f>" when the
// row has one.
func formatAWSLogLine(ev domain.SessionEvent) string {
	var p struct {
		Profile string `json:"profile"`
		State   string `json:"state"`
		Failure string `json:"failure"`
	}
	_ = json.Unmarshal(ev.Payload, &p)
	line := fmt.Sprintf("%s %s profile=%s state=%s", ev.At.Format(time.RFC3339), ev.Kind, p.Profile, p.State)
	if p.Failure != "" {
		line += " failure=" + p.Failure
	}
	return line
}

// runAWS prints the delegated permission set's policy, or its setup steps. A nil
// cfg is allowed: --regions then has to name the regions.
func runAWS(args []string, cfg *config.Config, out io.Writer) error {
	if len(args) > 0 && args[0] == "permission-set-apply" {
		return runPermissionSetApply(args[1:], cfg, out, os.Stderr)
	}
	if len(args) == 0 || args[0] != "permission-set-policy" {
		fmt.Fprint(os.Stderr, awsUsage)
		return errUsage
	}
	fs := flag.NewFlagSet("aws permission-set-policy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	regionsFlag := fs.String("regions", "", "comma-separated regions")
	name := fs.String("name", "", "permission set name")
	account := fs.String("account", "", "AWS account id")
	steps := fs.Bool("steps", false, "print the setup steps")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		fmt.Fprint(os.Stderr, awsUsage)
		return errUsage
	}

	regions := strings.Split(*regionsFlag, ",")
	if awsdelegation.PermissionSetPolicy(regions) == "" {
		regions = delegationRegions(cfg)
	}
	policy := awsdelegation.PermissionSetPolicy(regions)
	if policy == "" {
		return errors.New("no AWS delegation with regions in the config: pass --regions a,b")
	}
	if *steps {
		_, err := fmt.Fprint(out, awsdelegation.PermissionSetSteps(*name, *account, regions))
		return err
	}
	_, err := fmt.Fprintln(out, policy)
	return err
}

// runPermissionSetApply runs awsdelegation.Apply, or prints its Plan with
// --dry-run. Progress goes to progress so out holds only the result. With
// --remote the delegation is written into the laptop config after a
// successful apply, and the remote is checked before any aws call.
func runPermissionSetApply(args []string, cfg *config.Config, out, progress io.Writer) error {
	fs := flag.NewFlagSet("aws permission-set-apply", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	profile := fs.String("profile", "", "admin profile in ~/.aws/config")
	account := fs.String("account", "", "AWS account id; default the admin profile's own")
	regionsFlag := fs.String("regions", "", "comma-separated regions")
	name := fs.String("name", awsdelegation.DefaultPermissionSetName, "permission set name")
	user := fs.String("user", "", "Identity Center user name to assign")
	remote := fs.String("remote", "", "remote in ~/.aiman/config.yaml to write the delegation to")
	delegationProfile := fs.String("delegation-profile", "", "profile name of the written delegation; default the admin profile")
	dryRun := fs.Bool("dry-run", false, "print the aws calls and make none")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || strings.TrimSpace(*profile) == "" {
		fmt.Fprint(os.Stderr, awsUsage)
		return errUsage
	}
	regions := splitRegions(*regionsFlag)
	if awsdelegation.PermissionSetPolicy(regions) == "" {
		regions = delegationRegions(cfg)
	}
	spec := awsdelegation.ApplySpec{
		Profile:   strings.TrimSpace(*profile),
		AccountID: strings.TrimSpace(*account),
		Name:      *name,
		User:      strings.TrimSpace(*user),
		Regions:   regions,
	}
	target, err := newDelegationTarget(cfg, strings.TrimSpace(*remote), strings.TrimSpace(*delegationProfile), spec.Profile)
	if err != nil {
		return err
	}
	if *dryRun {
		if err := printApplyPlan(spec, out); err != nil {
			return err
		}
		return target.printPlanned(spec, out)
	}
	res, err := awsdelegation.Apply(context.Background(), newAWSCLI(spec.Profile), spec, progress)
	if err != nil {
		return err
	}
	if target == nil {
		return printApplyResult(res, *asJSON, out)
	}
	path, err := target.write(spec, res.Delegation)
	if err != nil {
		return err
	}
	return target.printWritten(res, path, *asJSON, out)
}

// printApplyResult prints res as JSON, or the Done. block with the delegation
// lines to copy into the config.
func printApplyResult(res awsdelegation.ApplyResult, asJSON bool, out io.Writer) error {
	if asJSON {
		return json.NewEncoder(out).Encode(res)
	}
	d := res.Delegation
	_, err := fmt.Fprintf(out, "Done. Put this in remotes[].aws_delegation for that account:\n"+
		"  sso_start_url: %s\n  sso_region: %s\n  sso_account_id: %s\n  sso_role_name: %s\n"+
		"Then push the config to the host (Agent API page, c) and sign in from the phone.\n",
		d.StartURL, d.Region, d.AccountID, d.RoleName)
	return err
}

// delegationTarget is where --remote writes the delegation: the remote's name
// as the config spells it and the entry's profile name.
type delegationTarget struct {
	cfg     *config.Config
	remote  string
	profile string
}

// newDelegationTarget checks --remote against cfg. No --remote is a nil target
// and no error; a nil cfg or an unknown remote is an error.
func newDelegationTarget(cfg *config.Config, remote, delegationProfile, adminProfile string) (*delegationTarget, error) {
	if remote == "" {
		return nil, nil
	}
	if cfg == nil {
		reason := "it could not be loaded"
		if _, err := config.Load(); err != nil {
			reason = err.Error()
		}
		return nil, fmt.Errorf("--remote needs a readable config: %s", reason)
	}
	names := make([]string, 0, len(cfg.Remotes))
	for _, r := range cfg.Remotes {
		if strings.EqualFold(r.Name, remote) {
			profile := delegationProfile
			if profile == "" {
				profile = adminProfile
			}
			return &delegationTarget{cfg: cfg, remote: r.Name, profile: profile}, nil
		}
		names = append(names, r.Name)
	}
	return nil, fmt.Errorf("no remote %s in the config; remotes: %s", remote, strings.Join(names, ", "))
}

// entry is the aws_delegations entry for spec and the apply's SSO facts.
func (t *delegationTarget) entry(spec awsdelegation.ApplySpec, hint awsdelegation.DelegationHint) config.AWSDelegation {
	return config.AWSDelegation{
		Profile:         t.profile,
		SourceProfile:   spec.Profile,
		SyncCredentials: true,
		Regions:         spec.Regions,
		SSOStartURL:     hint.StartURL,
		SSORegion:       hint.Region,
		SSOAccountID:    hint.AccountID,
		SSORoleName:     hint.RoleName,
	}
}

// write upserts the entry on the remote and saves the config, returning its path.
func (t *delegationTarget) write(spec awsdelegation.ApplySpec, hint awsdelegation.DelegationHint) (string, error) {
	if err := t.cfg.UpsertAWSDelegation(t.remote, t.entry(spec, hint)); err != nil {
		return "", err
	}
	if err := t.cfg.Save(); err != nil {
		return "", fmt.Errorf("the Identity Center setup is done, but writing the delegation failed; rerunning with --remote is safe: %w", err)
	}
	return config.GetConfigPath()
}

// printPlanned prints, for --dry-run, the entry a run would write. Its SSO
// facts are what is known without calling aws: the admin profile's start URL
// and region, --account and the permission set name. A nil target prints
// nothing.
func (t *delegationTarget) printPlanned(spec awsdelegation.ApplySpec, out io.Writer) error {
	if t == nil {
		return nil
	}
	hint := awsdelegation.DelegationHint{AccountID: spec.AccountID, RoleName: spec.Name}
	if home, err := os.UserHomeDir(); err == nil {
		if sso, err := awsdelegation.ReadSSOProfile(filepath.Join(home, ".aws", "config"), spec.Profile); err == nil {
			hint.StartURL, hint.Region = sso.StartURL, sso.Region
		}
	}
	data, err := yaml.Marshal(t.entry(spec, hint))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "# aws_delegations entry this command would write; the account id comes from the apply when --account is not given.\nremote %s:\n%s", t.remote, indentLines(string(data), "  "))
	return err
}

// writtenDelegation names the entry --remote wrote, for --json.
type writtenDelegation struct {
	Remote  string `json:"remote"`
	Profile string `json:"profile"`
	Path    string `json:"path"`
}

// writtenApplyResult is the --json output with --remote: the ApplyResult
// fields plus "config".
type writtenApplyResult struct {
	awsdelegation.ApplyResult
	Config writtenDelegation `json:"config"`
}

// printWritten reports the written entry: res plus a "config" object with
// --json, else one line naming the entry, the remote and the file.
func (t *delegationTarget) printWritten(res awsdelegation.ApplyResult, path string, asJSON bool, out io.Writer) error {
	if asJSON {
		return json.NewEncoder(out).Encode(writtenApplyResult{
			ApplyResult: res,
			Config:      writtenDelegation{Remote: t.remote, Profile: t.profile, Path: path},
		})
	}
	_, err := fmt.Fprintf(out, "Wrote aws_delegations entry %s on remote %s in %s. Press c on the Agent API page to push it to the host.\n", t.profile, t.remote, path)
	return err
}

// indentLines prefixes every non-empty line of s with prefix.
func indentLines(s, prefix string) string {
	lines := strings.SplitAfter(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "")
}

// printApplyPlan prints every aws call permission-set-apply may make, one
// "aws --profile <p> ..." line each, with shell quoting for pasting.
func printApplyPlan(spec awsdelegation.ApplySpec, out io.Writer) error {
	steps, err := awsdelegation.Plan(spec)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "# Every aws call this command may make. A run skips the ones its lookups make unnecessary:\n"+
		"# a first apply creates and never provisions; a rerun provisions and never creates."); err != nil {
		return err
	}
	for _, s := range steps {
		quoted := make([]string, len(s.Args))
		for i, a := range s.Args {
			quoted[i] = planQuote(a)
		}
		if _, err := fmt.Fprintf(out, "aws %s\n", strings.Join(quoted, " ")); err != nil {
			return err
		}
	}
	return nil
}

// planQuote leaves plain words and <placeholders> readable and shell-quotes the
// rest, such as the policy and the alternate identifier.
func planQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.=,:/@<>") == "" {
		return s
	}
	return shellx.Quote(s)
}

// splitRegions splits a --regions value on commas, trimming each region and
// dropping empty ones, so the policy and the saved delegation use one list.
func splitRegions(v string) []string {
	var out []string
	for _, r := range strings.Split(v, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

// delegationRegions returns the regions of the first delegation that has any,
// across all remotes.
func delegationRegions(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	for _, remote := range cfg.Remotes {
		for _, d := range remote.AllDelegations() {
			if d != nil && awsdelegation.PermissionSetPolicy(d.Regions) != "" {
				return d.Regions
			}
		}
	}
	return nil
}

// loadAWSCommandConfig reads the config for `aiman aws`. --regions works without
// one, so a config that cannot be read is a warning on stderr and a nil result.
func loadAWSCommandConfig() *config.Config {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "aiman aws: warning: %v\n", err)
		return nil
	}
	if cfg.PermissionsTightened {
		fmt.Fprintf(os.Stderr, "aiman aws: warning: %s was group/world-readable and has been tightened to 0600.\n", configPathForNotice())
	}
	return cfg
}
