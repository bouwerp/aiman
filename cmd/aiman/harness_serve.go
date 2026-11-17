package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/review"
	"github.com/bouwerp/aiman/internal/server"
)

// wireHarness enables the harness.* methods when harness.enabled is not
// false and docker is on PATH. It takes down runs a previous serve left
// behind, reaps idle runs until ctx ends, and returns the manager's closer
// (nil when the harness is off). The edge runs this binary, which must be
// statically linked, as release builds are.
func wireHarness(ctx context.Context, srv *server.Server, cfg *config.Config, dir string) func() {
	if !cfg.Harness.On() {
		return nil
	}
	if _, err := exec.LookPath("docker"); err != nil {
		log.Printf("harness unavailable: docker is not on PATH")
		return nil
	}
	bin, err := os.Executable()
	if err == nil {
		bin, err = filepath.EvalSymlinks(bin)
	}
	if err != nil {
		log.Printf("harness unavailable: locate the aiman binary: %v", err)
		return nil
	}
	sessionLimits, _ := cfg.Resources.SessionLimits()
	runMemory, warns := cfg.Harness.RunMemoryBytes(sessionLimits.MemoryHigh)
	serviceMemory, more := cfg.Harness.ServiceMemoryBytes()
	for _, w := range append(warns, more...) {
		log.Printf("config: %s", w)
	}
	m, err := harness.NewManager(harness.Options{
		Dir:           filepath.Join(dir, "harness"),
		AimanBin:      bin,
		UID:           os.Getuid(),
		GID:           os.Getgid(),
		AllowedImages: cfg.Harness.AllowedImages,
		ServiceMemory: serviceMemory,
		RunMemory:     runMemory,
		MaxRuns:       cfg.Harness.MaxRunsOrDefault(),
		IdleTimeout:   cfg.Harness.IdleTimeoutOrDefault(),
		Docker:        harness.ExecDocker{},
		AppImage:      review.Image,
		Record:        srv.HarnessRecorder(),
	})
	if err != nil {
		log.Printf("harness unavailable: %v", err)
		return nil
	}
	srv.SetHarness(server.HarnessDeps{Manager: m, Repos: cfg.Repos})
	go m.CleanStale(ctx)
	go m.RunIdle(ctx)
	return m.Close
}
