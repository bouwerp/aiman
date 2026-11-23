package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/remotesvc"
	"github.com/bouwerp/aiman/internal/ui"
)

const updatedFromEnv = "AIMAN_UPDATED_FROM"

// restartArgv is the argv for the exec that follows a self-update: the
// resolved binary, then the arguments the process was started with (before
// global flags were stripped).
func restartArgv(resolved string, origArgs []string) []string {
	argv := []string{resolved}
	if len(origArgs) > 1 {
		argv = append(argv, origArgs[1:]...)
	}
	return argv
}

// restartEnv returns a copy of env holding exactly one AIMAN_UPDATED_FROM.
func restartEnv(env []string, from string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, updatedFromEnv+"=") {
			out = append(out, kv)
		}
	}
	return append(out, updatedFromEnv+"="+from)
}

// managedInstall reports the package manager that owns path, whose files the
// manager (not aiman) must replace.
func managedInstall(path string) (manager string, ok bool) {
	switch {
	case strings.Contains(path, "/Cellar/"),
		strings.HasPrefix(path, "/opt/homebrew/"),
		strings.HasPrefix(path, "/home/linuxbrew/.linuxbrew/"):
		return "Homebrew", true
	case strings.HasPrefix(path, "/nix/store/"):
		return "nix", true
	}
	return "", false
}

// resolveSelf returns the running binary's path with symlinks resolved.
func resolveSelf() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot determine current executable path: %w", err)
	}
	p, err = filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("cannot resolve executable symlink: %w", err)
	}
	return p, nil
}

// tuiUpdater stages releases for the TUI's update page.
type tuiUpdater struct{}

type stagedBinary struct {
	target, tmp string
}

func (s *stagedBinary) Target() string { return s.target }

func (s *stagedBinary) Apply() error {
	if err := replaceExecutable(s.target, s.tmp); err != nil {
		return err
	}
	_ = os.Remove(s.tmp)
	return nil
}

func (s *stagedBinary) Discard() { _ = os.Remove(s.tmp) }

// Stage downloads and verifies tag (the latest release when empty) without
// touching the installed binary. It prints nothing; progress goes to the page.
func (tuiUpdater) Stage(ctx context.Context, tag string, progress func(string)) (ui.Staged, error) {
	if runtime.GOOS == "windows" {
		return nil, errors.New("quit, then run aiman update")
	}
	target, err := resolveSelf()
	if err != nil {
		return nil, err
	}
	if mgr, ok := managedInstall(target); ok {
		return nil, fmt.Errorf("installed by %s: update it with %s", mgr, strings.ToLower(mgr))
	}
	probe, err := os.CreateTemp(filepath.Dir(target), ".aiman-update-probe-*")
	if err != nil {
		return nil, fmt.Errorf("cannot write to %s: run sudo aiman update", filepath.Dir(target))
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())

	var release *githubRelease
	if tag == "" {
		release, err = fetchLatestRelease(ctx)
	} else {
		release, err = fetchReleaseByTag(ctx, tag)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find release: %w", err)
	}
	if tag == "" && !newerThanRunning(release.TagName) {
		return nil, fmt.Errorf("aiman %s is up to date", version)
	}
	asset := platformAssetName()
	progress("download " + asset + ".tar.gz")
	tmp, err := downloadRelease(ctx, release, asset)
	if err != nil {
		return nil, err
	}
	progress("verify " + checksumsAsset)
	return &stagedBinary{target: target, tmp: tmp}, nil
}

// newerThanRunning reports whether tag is a release newer than this binary.
// A build whose version is not a release (dev) takes any release.
func newerThanRunning(tag string) bool {
	cur, ok := remotesvc.ParseVersion(version)
	if !ok {
		return true
	}
	t, ok := remotesvc.ParseVersion(tag)
	return ok && cur.Less(t)
}
