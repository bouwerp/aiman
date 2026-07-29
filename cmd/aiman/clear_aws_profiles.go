package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

// runClearAWSProfiles removes legacy session-scoped "aiman-<id>" AWS profile names from
// stored sessions. Opening the database already performs the cleanup, so this command
// reports what that migration removed plus anything it clears itself, and then lists the
// AWS profiles that actually exist locally for the sessions to fall back to.
func runClearAWSProfiles(db *sqlite.Repository, args []string) error {
	fs := flag.NewFlagSet("clear-aws-profiles", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: aiman clear-aws-profiles\n\n")
		fmt.Fprintf(os.Stderr, "Clears legacy session-scoped \"aiman-<id>\" AWS profile names from stored\n")
		fmt.Fprintf(os.Stderr, "sessions. Affected sessions fall back to the configured default profile;\n")
		fmt.Fprintf(os.Stderr, "the rest of each session's AWS config (region, role, policy) is kept.\n")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	ctx := context.Background()
	cleared := db.LegacyAWSProfilesClearedOnOpen()

	// The open-time migration normally handles everything; this pass catches rows written
	// by another process since then.
	extra, err := db.ClearLegacyAWSProfiles(ctx)
	if err != nil {
		return err
	}
	cleared = append(cleared, extra...)

	if len(cleared) == 0 {
		fmt.Println("No legacy aiman-* AWS profiles found in stored sessions.")
	} else {
		sort.Slice(cleared, func(i, j int) bool {
			if cleared[i].SessionID != cleared[j].SessionID {
				return cleared[i].SessionID < cleared[j].SessionID
			}
			return cleared[i].Field < cleared[j].Field
		})
		fmt.Printf("Cleared %d legacy AWS profile reference(s):\n", len(cleared))
		for _, ref := range cleared {
			fmt.Printf("  %s\n", ref)
		}
		fmt.Println("\nAffected sessions now fall back to the configured default profile.")
		fmt.Println("Restart them (or re-set their AWS profile) to pick up the change.")
	}

	remaining, err := db.FindLegacyAWSProfiles(ctx)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return fmt.Errorf("%d legacy AWS profile reference(s) survived the cleanup", len(remaining))
	}

	if names, err := awsdelegation.ListLocalAWSProfileNames(); err == nil && len(names) > 0 {
		fmt.Printf("\nLocal AWS profiles available: %v\n", names)
	}
	return nil
}
