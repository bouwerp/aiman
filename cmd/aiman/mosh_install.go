package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// moshInstallTimeout bounds a package-manager run so a stuck mirror cannot
// keep a root child alive for the life of the process. The API is already
// serving while this runs.
const moshInstallTimeout = 3 * time.Minute

// aptMoshInstall is one root shell so update and install share a single
// non-interactive sudo. sudo -n fails immediately when a password is required
// instead of blocking serve's startup on a prompt nobody can see.
const aptMoshInstall = `set -eu
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y mosh`

// hostHasTool reports whether name is executable. mosh-server is also sought
// on the extra directories extendServePath adds, because a systemd user unit
// starts with a short PATH.
var hostHasTool = defaultHostHasTool

// runHostCmd executes a fixed install command. Tests replace it.
var runHostCmd = defaultRunHostCmd

var extendServePathOnce sync.Once

func ensureMosh(ctx context.Context) {
	bin, args, skip, err := moshInstallPlan(hostHasTool)
	if err != nil {
		log.Printf("mosh: %v", err)
		return
	}
	if skip {
		log.Printf("mosh: mosh-server already installed")
		return
	}
	go installMosh(ctx, bin, args)
}

func installMosh(ctx context.Context, bin string, args []string) {
	ctx, cancel := context.WithTimeout(ctx, moshInstallTimeout)
	defer cancel()
	log.Printf("mosh: mosh-server not found, installing with %s", bin)
	if err := runHostCmd(ctx, bin, args...); err != nil {
		fb, fbArgs, ok := userMoshInstall(hostHasTool)
		if !ok {
			log.Printf("mosh: install failed (serve continues without it): %v", err)
			return
		}
		log.Printf("mosh: %s failed (%v); extracting the package into ~/.local/bin", bin, err)
		if err := runHostCmd(ctx, fb, fbArgs...); err != nil {
			log.Printf("mosh: user install failed (serve continues without it): %v", err)
			return
		}
	}
	if !hostHasTool("mosh-server") {
		log.Printf("mosh: install command finished but mosh-server is still not installed")
		return
	}
	log.Printf("mosh: mosh-server installed")
}

// userMoshInstall unpacks the distro package into ~/.local/bin. apt-get
// download and dpkg-deb -x do not need root, and this host already has the
// shared libraries the Debian mosh package links. sudo -n cannot prompt.
const userMoshInstallScript = `set -eu
umask 022
mkdir -p "${HOME}/.local/bin"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$work"
apt-get download mosh
deb=$(ls mosh_*.deb)
dpkg-deb -x "$deb" "$work/root"
test -x "$work/root/usr/bin/mosh-server"
cp "$work/root/usr/bin/mosh-server" "${HOME}/.local/bin/mosh-server"
chmod 755 "${HOME}/.local/bin/mosh-server"`

func userMoshInstall(have func(string) bool) (string, []string, bool) {
	if !have("apt-get") || !have("dpkg-deb") {
		return "", nil, false
	}
	return "sh", []string{"-c", userMoshInstallScript}, true
}

// moshInstallPlan chooses how to install mosh, or skips when mosh-server is
// already executable. System packages come before Homebrew: the mosh client
// starts the remote server as a bare `mosh-server` on SSH's default PATH,
// and a Homebrew prefix is not on that PATH.
func moshInstallPlan(have func(string) bool) (bin string, args []string, skip bool, err error) {
	if have("mosh-server") {
		return "", nil, true, nil
	}
	switch {
	case have("apt-get"):
		return "sudo", []string{"-n", "sh", "-c", aptMoshInstall}, false, nil
	case have("dnf"):
		return "sudo", []string{"-n", "dnf", "install", "-y", "mosh"}, false, nil
	case have("yum"):
		return "sudo", []string{"-n", "yum", "install", "-y", "mosh"}, false, nil
	case have("pacman"):
		return "sudo", []string{"-n", "pacman", "-S", "--noconfirm", "mosh"}, false, nil
	case have("apk"):
		return "sudo", []string{"-n", "apk", "add", "--no-cache", "mosh"}, false, nil
	case have("brew"):
		return "brew", []string{"install", "mosh"}, false, nil
	default:
		return "", nil, false, errors.New("mosh-server is not installed and no supported package manager was found (apt-get, dnf, yum, pacman, apk, brew)")
	}
}

func defaultHostHasTool(name string) bool {
	extendServePathOnce.Do(extendServePath)
	_, err := exec.LookPath(name)
	return err == nil
}

func extendServePath() {
	home, _ := os.UserHomeDir()
	extras := []string{"/opt/homebrew/bin", "/usr/local/bin"}
	if home != "" {
		extras = append(extras,
			filepath.Join(home, ".local/bin"),
			filepath.Join(home, ".linuxbrew/bin"),
			filepath.Join(home, "linuxbrew/.linuxbrew/bin"),
		)
	}
	prefix := strings.Join(extras, string(os.PathListSeparator))
	current := os.Getenv("PATH")
	if current == "" {
		_ = os.Setenv("PATH", prefix)
		return
	}
	_ = os.Setenv("PATH", prefix+string(os.PathListSeparator)+current)
}

func defaultRunHostCmd(ctx context.Context, name string, args ...string) error {
	// Name and args are chosen by moshInstallPlan from a fixed list.
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: installer argv is not user input
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w\n%s", name, err, trimOutput(out))
	}
	return nil
}

func trimOutput(out []byte) string {
	const max = 2000
	s := strings.TrimSpace(string(out))
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}
