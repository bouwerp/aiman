package cigate

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// unitUnsafe matches what systemd does not take in a unit name; it is
// cgroup.ScopeName's rule.
var unitUnsafe = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// CIUnit is the transient scope of one CI run:
// aiman-ci-<session>-<sha[:12]>-<start unix seconds>.scope. The start time
// keeps a leaked scope of an earlier run of the same commit from colliding.
func CIUnit(session, sha string, start time.Time) string {
	return unitName("aiman-ci-" + session + "-" + prefix(sha, 12) + "-" + strconv.FormatInt(start.Unix(), 10))
}

// CIUnitFor is CIUnit for one member of a workspace session:
// aiman-ci-<session>-<repo key>-<sha[:12]>-<start>.scope. An empty repo is
// CIUnit.
func CIUnitFor(session, repo, sha string, start time.Time) string {
	if repo == "" {
		return CIUnit(session, sha, start)
	}
	return unitName("aiman-ci-" + session + "-" + RepoKey(repo) + "-" + prefix(sha, 12) + "-" + strconv.FormatInt(start.Unix(), 10))
}

// VerifyUnit is the transient scope of one verify test command:
// aiman-verify-<review>-<start unix seconds>.scope.
func VerifyUnit(review string, start time.Time) string {
	return unitName("aiman-verify-" + review + "-" + strconv.FormatInt(start.Unix(), 10))
}

func unitName(base string) string {
	clean := unitUnsafe.ReplaceAllString(base, "_")
	if len(clean) > 240 {
		clean = clean[:240]
	}
	return clean + ".scope"
}

func prefix(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// SystemdLauncher runs each command through systemd-run --user --scope
// --collect in the given unit, under slice with the given properties
// (cgroup.Limits.Properties). The scope belongs to the run, not to a
// session, so a build cannot throttle the agent waiting on it. Without
// systemd-run on PATH the command runs plainly.
func SystemdLauncher(slice string, props []string) Launcher {
	return func(unit string, cmd *exec.Cmd) {
		bin, err := exec.LookPath("systemd-run")
		if err != nil {
			return
		}
		argv := []string{bin, "--user", "--scope", "--quiet", "--collect", "--unit=" + unit}
		if slice != "" {
			argv = append(argv, "--slice="+slice)
		}
		for _, p := range props {
			argv = append(argv, "-p", p)
		}
		argv = append(argv, "--", cmd.Path)
		cmd.Path, cmd.Args = bin, append(argv, cmd.Args[1:]...)
	}
}

// tmpInUse holds the run directories ClaimTmp handed out and not yet
// released, by absolute path.
var tmpInUse sync.Map

// ClaimTmp makes base/name a fresh, empty directory (0700) for one run and
// returns it with a release that removes it. A leftover of an earlier run
// is removed first. TmpInUse reports the directory until release.
func ClaimTmp(base, name string) (string, func(), error) {
	dir, err := filepath.Abs(filepath.Join(base, name))
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", nil, err
	}
	_ = os.RemoveAll(dir)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", nil, err
	}
	tmpInUse.Store(dir, true)
	return dir, func() {
		_ = os.RemoveAll(dir)
		tmpInUse.Delete(dir)
	}, nil
}

// TmpInUse reports whether dir is a run directory ClaimTmp handed out and a
// run still uses. The scratch janitor never removes one.
func TmpInUse(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	_, ok := tmpInUse.Load(abs)
	return ok
}
