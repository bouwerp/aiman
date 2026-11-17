package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	harnessscanner "github.com/bouwerp/aiman/build/harness-scanner"
	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/images"
	"github.com/bouwerp/aiman/internal/harness/tools"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/review"
	"github.com/bouwerp/aiman/internal/server"
)

// wireHarness enables the harness.* methods when harness.enabled is not
// false and docker is on PATH. It takes down runs a previous serve left
// behind, reaps idle runs, vets images and keeps the scanner databases fresh
// until ctx ends, and returns the manager's closer (nil when the harness is
// off). The edge and the scanner container run this binary, which must be
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
	hdir := filepath.Join(dir, "harness")
	vetter, maintainer, err := wireImageVetting(srv, cfg, hdir, bin)
	if err != nil {
		log.Printf("harness unavailable: image vetting: %v", err)
		return nil
	}
	pin, err := harness.NewDlvPin(cfg.Harness.DlvVersion, cfg.Harness.DlvModuleSum)
	if err != nil {
		log.Printf("config: %v; using Delve %s", err, harness.DefaultDlv.Version)
		pin = harness.DefaultDlv
	}
	m, err := harness.NewManager(harness.Options{
		DebugAdapters: []harness.DebugAdapter{
			&harness.GoDebug{Tools: &harness.DlvTools{Dir: filepath.Join(hdir, "tools"), Pin: pin}},
			&harness.NodeDebug{
				Tools:   &tools.Store{Dir: filepath.Join(hdir, "tools")},
				Node:    toolPin(cfg.Harness.DebugTools["node"]),
				JSDebug: toolPin(cfg.Harness.DebugTools["js-debug"]),
			},
			&harness.BunDebug{
				Tools:   &tools.Store{Dir: filepath.Join(hdir, "tools")},
				Bun:     toolPin(cfg.Harness.DebugTools["bun"]),
				DAP:     toolPin(cfg.Harness.DebugTools["bun-dap"]),
				Release: version,
			},
			&harness.PythonDebug{
				Tools:   &tools.Store{Dir: filepath.Join(hdir, "tools")},
				Debugpy: toolPin(cfg.Harness.DebugTools["debugpy"]),
			},
		},
		Images:        vetter,
		Dir:           hdir,
		AimanBin:      bin,
		UID:           os.Getuid(),
		GID:           os.Getgid(),
		AllowedImages: cfg.Harness.AllowedImages,
		ServiceMemory: serviceMemory,
		RunMemory:     runMemory,
		MaxRuns:       cfg.Harness.MaxRunsOrDefault(),
		IdleTimeout:   cfg.Harness.IdleTimeoutOrDefault(),
		DBRowLimit:    cfg.Harness.DB.RowLimit,
		Docker:        harness.ExecDocker{},
		AppImage:      review.Image,
		Record:        srv.HarnessRecorder(),
	})
	if err != nil {
		log.Printf("harness unavailable: %v", err)
		return nil
	}
	srv.SetHarness(server.HarnessDeps{Manager: m, Repos: cfg.Repos, Images: vetter})
	go m.CleanStale(ctx)
	go m.RunIdle(ctx)
	go func() {
		vetter.Resume(ctx)
		vetter.Run(ctx)
	}()
	go maintainer.Run(ctx)
	return func() {
		m.Close()
		vetter.Wait()
		srv.WaitHarnessImages()
	}
}

// wireImageVetting builds the image vetter: the registry client, the
// scanner container (built on the host from the pinned Dockerfile), the
// docker loader, serve's notifier and the pressure gate, plus the
// maintainer that refreshes the scanner databases and re-scans SBOMs.
func wireImageVetting(srv *server.Server, cfg *config.Config, dir, bin string) (*images.Vetter, *images.Maintainer, error) {
	ic := cfg.Harness.Images
	maxSize, warns := ic.MaxSizeBytes()
	args, more := ic.ScannerBuildArgs()
	for _, w := range append(warns, more...) {
		log.Printf("config: %s", w)
	}
	store, err := images.NewStore(dir)
	if err != nil {
		return nil, nil, err
	}
	scannerImage := &images.ScannerImage{Docker: images.ExecDockerCLI, Dockerfile: harnessscanner.Dockerfile, BuildArgs: args, Dir: dir}
	dbDir := filepath.Join(dir, "db")
	var reviewer images.Reviewer
	if ic.ReviewOn() {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, err
		}
		// The review container reuses the scanner image (glibc and CA
		// certificates); the agent binary and a credential copy are mounted
		// read-only, and the proxy runs this binary.
		rv, err := images.NewModelReviewer(images.ReviewOptions{
			Dir: dir, Smart: func() []config.PreferredAgent { return cfg.Agents.Smart },
			LookPath: exec.LookPath, SessionAgent: srv.HarnessSessionAgent,
			Credentials: images.HostCredentials{Home: home}.For,
			Runner:      images.DockerReviewRunner{}, Image: scannerImage.Ensure,
			AimanBin: bin, UID: os.Getuid(), GID: os.Getgid(),
		})
		if err != nil {
			return nil, nil, err
		}
		reviewer = rv
	}
	v, err := images.NewVetter(images.VetterOptions{
		Dir: dir, Store: store, Registry: &images.HTTPRegistry{},
		Scanner: images.DockerScanner{ImageFor: scannerImage.Ensure, AimanBin: bin, DBDir: dbDir,
			ScratchDir: filepath.Join(dir, "scan"), UID: os.Getuid(), GID: os.Getgid()},
		Loader: images.DockerLoader{}, Notify: srv.HarnessImageNotifier(), Reviewer: reviewer,
		AllowedImages: cfg.Harness.AllowedImages, CVEBlock: ic.CVEBlockOrDefault(),
		MaxSize: maxSize, ScanTimeout: ic.ScanTimeoutOrDefault(), MayStart: srv.PressureOpen,
	})
	if err != nil {
		return nil, nil, err
	}
	mt := &images.Maintainer{Vetter: v, Image: scannerImage, Every: ic.DBRefreshOrDefault(), Refresher: images.Refresher{
		DBDir: dbDir, UID: os.Getuid(), GID: os.Getgid(), Docker: images.ExecDockerCLI,
	}}
	return v, mt, nil
}

// toolPin is a harness.debug_tools entry as the tools package takes it.
func toolPin(p config.HarnessToolPin) tools.Pin {
	return tools.Pin{Version: p.Version, SHA256: p.SHA256, URL: p.URL}
}
