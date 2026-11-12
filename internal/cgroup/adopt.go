package cgroup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// adoptPasses bounds how often Adopt looks again for processes forked while
// it was moving their parents.
const adoptPasses = 5

// adoptWait is how long Adopt waits for the user manager to finish creating
// the scope and moving the first processes into it.
const adoptWait = 5 * time.Second

// Of is the cgroup directory pid runs in, or "" when pid is gone.
func Of(pid int) string {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok {
			return filepath.Join(fsRoot, path)
		}
	}
	return ""
}

// adoptArgv is the busctl call that asks the user manager for a new scope
// holding pids, with the limits l. systemd-run can only put itself in a
// scope; StartTransientUnit's PIDs property moves processes that already run.
func adoptArgv(id string, l Limits, pids []int) []string {
	type prop struct{ name, sig, value string }
	props := []prop{
		{"Slice", "s", SessionsSlice},
		{"Description", "s", "aiman session " + id},
		{"CollectMode", "s", "inactive-or-failed"},
	}
	size := func(name string, v int64) {
		switch {
		case v < 0:
			props = append(props, prop{name, "t", "18446744073709551615"})
		case v > 0:
			props = append(props, prop{name, "t", strconv.FormatInt(v, 10)})
		}
	}
	num := func(name string, v int) {
		if v > 0 {
			props = append(props, prop{name, "t", strconv.Itoa(v)})
		}
	}
	size("MemoryHigh", l.MemoryHigh)
	size("MemoryMax", l.MemoryMax)
	size("MemorySwapMax", l.MemorySwapMax)
	num("CPUWeight", l.CPUWeight)
	num("IOWeight", l.IOWeight)
	num("TasksMax", l.TasksMax)

	argv := []string{
		"--user", "call", "org.freedesktop.systemd1", "/org/freedesktop/systemd1",
		"org.freedesktop.systemd1.Manager", "StartTransientUnit", "ssa(sv)a(sa(sv))",
		ScopeName(id), "fail", strconv.Itoa(len(props) + 1),
		"PIDs", "au", strconv.Itoa(len(pids)),
	}
	for _, pid := range pids {
		argv = append(argv, strconv.Itoa(pid))
	}
	for _, p := range props {
		argv = append(argv, p.name, p.sig, p.value)
	}
	return append(argv, "0")
}

// Adopt moves the running processes pids returns into the session id's
// scope, creating the scope with the limits l when it does not exist and
// applying l to it when it does. pids is asked again after each pass, so a
// process forked while its parent was being moved is caught; a process that
// forks faster than adoptPasses passes can still leave a child behind. It
// returns how many processes it moved: 0 when every one was already inside.
func (s *Systemd) Adopt(ctx context.Context, id string, l Limits, pids func() []int) (int, error) {
	dir := s.ScopeDir(id)
	outside := func() []int {
		var out []int
		for _, pid := range pids() {
			if at := Of(pid); at != "" && at != dir {
				out = append(out, pid)
			}
		}
		return out
	}
	todo := outside()
	if len(todo) == 0 {
		return 0, nil
	}
	seen := map[int]bool{}
	for _, pid := range todo {
		seen[pid] = true
	}
	if s.Active(id) {
		if err := s.SetLimits(ctx, id, l); err != nil {
			return 0, err
		}
	} else if err := s.startWith(ctx, id, l, todo, outside); err != nil {
		return 0, err
	}
	var last error
	for range adoptPasses {
		if todo = outside(); len(todo) == 0 {
			break
		}
		for _, pid := range todo {
			seen[pid] = true
			if err := moveInto(dir, pid); err != nil {
				last = err
			}
		}
	}
	left := outside()
	if len(left) > 0 {
		if last == nil {
			last = errors.New("they keep forking")
		}
		return len(seen) - len(left), fmt.Errorf("cgroup: %d processes stayed outside %s: %w", len(left), ScopeName(id), last)
	}
	return len(seen), nil
}

// startWith creates the scope around first and waits until the manager has
// moved them: StartTransientUnit only queues the job.
func (s *Systemd) startWith(ctx context.Context, id string, l Limits, first []int, outside func() []int) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := s.run(cctx, "busctl", adoptArgv(id, l, first)...); err != nil {
		return fmt.Errorf("cgroup: starting %s: %s", ScopeName(id), firstLine(out, err))
	}
	deadline := time.Now().Add(adoptWait)
	for {
		if s.Active(id) {
			waiting := map[int]bool{}
			for _, pid := range outside() {
				waiting[pid] = true
			}
			done := true
			for _, pid := range first {
				done = done && !waiting[pid]
			}
			if done {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cgroup: %s did not take its processes within %s", ScopeName(id), adoptWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// moveInto writes pid into dir's cgroup.procs. A process that has exited is
// not an error.
func moveInto(dir string, pid int) error {
	err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0o644) //nolint:gosec // G306: a cgroup control file, mode is ignored
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
