package main

import (
	"context"
	"log"
	"path/filepath"

	"github.com/bouwerp/aiman/internal/cgroup"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/server"
)

// wireResources applies the resources block: every terminal serve creates
// from here on runs in its session's systemd scope with the configured
// limits, under a sessions slice with one aggregate limit, and gets its own
// TMPDIR. A host without a user systemd (macOS, a container) keeps plain
// holders; that is logged once and is not an error.
func wireResources(ctx context.Context, srv *server.Server, mgr *ptyruntime.Manager, cfg config.ResourcesConfig, dir string) *cgroup.Systemd {
	limits, warns := cfg.SessionLimits()
	sliceHigh, sliceWarns := cfg.SliceMemoryHigh()
	for _, w := range append(warns, sliceWarns...) {
		log.Printf("config: %s", w)
	}
	opts := server.ResourceOptions{
		Limits:    limits,
		Overrides: &cgroup.Overrides{Dir: filepath.Join(dir, "session-limits")},
	}
	if cfg.TmpdirOn() {
		opts.TmpRoot = filepath.Join(dir, "tmp")
	}
	if !cfg.On() {
		log.Printf("resources: disabled; sessions run without their own limits")
		srv.SetResources(opts)
		return nil
	}
	sd, err := cgroup.Probe(ctx)
	if err != nil {
		log.Printf("resources: sessions run without their own limits: %v", err)
		srv.SetResources(opts)
		return nil
	}
	if sliceHigh == 0 {
		sliceHigh = cgroup.DefaultSliceMemoryHigh(sd.UserSliceMemoryHigh())
	}
	if err := sd.EnsureSlice(ctx, sliceHigh); err != nil {
		log.Printf("resources: sessions slice limit: %v", err)
	} else {
		log.Printf("resources: session scopes on; %s MemoryHigh=%s, each session high %s max %s",
			cgroup.SessionsSlice, sliceLabel(sliceHigh),
			cgroup.FormatSize(limits.MemoryHigh), cgroup.FormatSize(limits.MemoryMax))
	}
	opts.Systemd = sd
	opts.SliceMemoryHigh = sliceHigh
	mgr.Scopes = sd
	srv.SetResources(opts)
	return sd
}

func sliceLabel(high int64) string {
	if high <= 0 {
		return "none"
	}
	return cgroup.FormatSize(high)
}
