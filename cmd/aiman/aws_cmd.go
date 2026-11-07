package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
)

const awsUsage = "Usage: aiman aws permission-set-policy [--regions a,b] [--name NAME] [--account ID] [--steps]\n"

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
