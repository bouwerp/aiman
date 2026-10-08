package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMoshInstallPlanSkipsWhenServerExists(t *testing.T) {
	bin, args, skip, err := moshInstallPlan(func(string) bool { return true })
	if err != nil || !skip || bin != "" || args != nil {
		t.Fatalf("present server should skip, got bin=%q args=%v skip=%v err=%v", bin, args, skip, err)
	}
}

func TestMoshInstallPlanPrefersAptOverBrew(t *testing.T) {
	have := map[string]bool{"apt-get": true, "brew": true}
	bin, args, skip, err := moshInstallPlan(func(name string) bool { return have[name] })
	if err != nil || skip {
		t.Fatalf("apt plan: skip=%v err=%v", skip, err)
	}
	got := bin + " " + strings.Join(args, " ")
	if bin != "sudo" || !strings.Contains(got, "-n") || !strings.Contains(got, "apt-get install -y mosh") {
		t.Fatalf("apt install must be non-interactive sudo: %s", got)
	}
	if strings.Contains(got, "brew") {
		t.Fatalf("system package must win over brew so mosh-server is on the default SSH PATH: %s", got)
	}
}

func TestMoshInstallPlanUsesBrewWhenItIsTheOnlyManager(t *testing.T) {
	bin, args, skip, err := moshInstallPlan(func(name string) bool { return name == "brew" })
	if err != nil || skip || bin != "brew" || strings.Join(args, " ") != "install mosh" {
		t.Fatalf("brew plan: bin=%q args=%v skip=%v err=%v", bin, args, skip, err)
	}
}

func TestMoshInstallPlanReportsNoManager(t *testing.T) {
	_, _, skip, err := moshInstallPlan(func(string) bool { return false })
	if err == nil || skip {
		t.Fatalf("missing manager should fail closed, skip=%v err=%v", skip, err)
	}
}

func TestInstallMoshFallsBackToUserDebWhenSudoFails(t *testing.T) {
	prevHas := hostHasTool
	prevRun := runHostCmd
	installed := false
	var cmds []string
	hostHasTool = func(name string) bool {
		if name == "mosh-server" {
			return installed
		}
		return name == "apt-get" || name == "dpkg-deb"
	}
	runHostCmd = func(_ context.Context, bin string, args ...string) error {
		line := bin + " " + strings.Join(args, " ")
		cmds = append(cmds, line)
		if strings.Contains(line, "sudo") {
			return errors.New("sudo: a password is required")
		}
		if strings.Contains(line, "sudo") || !strings.Contains(line, "apt-get download mosh") || !strings.Contains(line, ".local/bin/mosh-server") {
			return errors.New("unexpected fallback: " + line)
		}
		installed = true
		return nil
	}
	t.Cleanup(func() {
		hostHasTool = prevHas
		runHostCmd = prevRun
	})

	installMosh(context.Background(), "sudo", []string{"-n", "sh", "-c", aptMoshInstall})
	if len(cmds) != 2 {
		t.Fatalf("expected sudo then a user extract, got %v", cmds)
	}
}

func TestEnsureMoshDoesNotRunWhenAlreadyInstalled(t *testing.T) {
	prevHas := hostHasTool
	prevRun := runHostCmd
	hostHasTool = func(string) bool { return true }
	runHostCmd = func(context.Context, string, ...string) error {
		t.Fatal("install ran even though mosh-server is present")
		return nil
	}
	t.Cleanup(func() {
		hostHasTool = prevHas
		runHostCmd = prevRun
	})
	ensureMosh(context.Background())
}

func TestEnsureMoshInstallsInBackground(t *testing.T) {
	prevHas := hostHasTool
	prevRun := runHostCmd
	installed := false
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var releaseOnce sync.Once
	var finishOnce sync.Once
	stopInstall := func() { releaseOnce.Do(func() { close(release) }) }
	hostHasTool = func(name string) bool {
		if name == "mosh-server" {
			if !installed {
				return false
			}
			// Close after this check returns, so cleanup does not swap the
			// hook while installMosh is still reading it.
			defer finishOnce.Do(func() { close(finished) })
			return true
		}
		return name == "apt-get"
	}
	runHostCmd = func(ctx context.Context, bin string, args ...string) error {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if bin != "sudo" || !strings.Contains(strings.Join(args, " "), "apt-get install -y mosh") {
			return errors.New("unexpected command")
		}
		installed = true
		return nil
	}
	t.Cleanup(func() {
		stopInstall()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
		}
		hostHasTool = prevHas
		runHostCmd = prevRun
	})

	returned := make(chan struct{})
	go func() {
		ensureMosh(context.Background())
		close(returned)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("install did not start")
	}
	select {
	case <-returned:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("ensureMosh blocked on the package install")
	}
	stopInstall()
}
