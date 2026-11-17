package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bouwerp/aiman/internal/harness/images"
)

// runHarnessScan is `aiman harness scan --plan FILE`, which serve runs inside
// the scanner container (no network, read-only root, the quarantine mounted
// read-only). It runs one stage of the plan and writes the stage's result
// beside it. It exits non-zero only when it could not write the result.
func runHarnessScan(args []string) error {
	fs := flag.NewFlagSet("harness scan", flag.ContinueOnError)
	planPath := fs.String("plan", "", "the scan plan serve wrote")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *planPath == "" {
		return errors.New("--plan is required")
	}
	b, err := os.ReadFile(*planPath)
	if err != nil {
		return err
	}
	var plan images.ScanPlan
	if err := json.Unmarshal(b, &plan); err != nil {
		return fmt.Errorf("read the scan plan: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := images.RunScan(ctx, plan, images.ExecTool)
	if err != nil {
		return err
	}
	if res.Error != "" {
		_, _ = fmt.Fprintln(os.Stderr, "harness scan:", res.Error)
	}
	return nil
}
