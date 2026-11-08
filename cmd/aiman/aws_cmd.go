package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
)

const awsUsage = "Usage: aiman aws permission-set-policy [--regions a,b] [--name NAME] [--account ID] [--steps]\n" +
	"       aiman aws log [--since SEQ] [--limit N] [--json]\n"

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
