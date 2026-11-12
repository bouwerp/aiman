package server

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/cgroup"
)

// ResourceOptions is how serve bounds each session's resource use.
type ResourceOptions struct {
	// Systemd is the host's user systemd. Nil (another OS, no user manager,
	// or resources.enabled false) leaves sessions without limits.
	Systemd *cgroup.Systemd
	// Limits are the default limits of one session scope.
	Limits cgroup.Limits
	// SliceMemoryHigh is the aggregate limit set on the sessions slice; 0
	// is none.
	SliceMemoryHigh int64
	// Overrides holds the limits the operator set for single sessions.
	Overrides *cgroup.Overrides
	// TmpRoot is the directory holding each session's TMPDIR; empty leaves
	// TMPDIR alone.
	TmpRoot string
}

// SetResources applies the resources block. Terminals created before the
// call keep what they started with.
func (s *Server) SetResources(o ResourceOptions) { s.resources.Store(&o) }

func (s *Server) resourceOptions() ResourceOptions {
	if o := s.resources.Load(); o != nil {
		return *o
	}
	return ResourceOptions{}
}

// scopeKey is the session whose cgroup scope and TMPDIR the pty id uses: the
// id itself, or, for a helper terminal named "<session id>-…" that has no
// row of its own, that session. A helper therefore spends its session's
// budget instead of getting a fresh one.
func (s *Server) scopeKey(ctx context.Context, id string) string {
	if s.repo == nil {
		return id
	}
	rows, err := s.storedSessions(ctx)
	if err != nil {
		return id
	}
	owner := ""
	for _, row := range rows {
		if row.ID == id {
			return id
		}
		if strings.HasPrefix(id, row.ID+"-") && len(row.ID) > len(owner) {
			owner = row.ID
		}
	}
	if owner != "" {
		return owner
	}
	return id
}

// hasRow reports whether id is a stored session. A failed read counts as a
// row, so nothing of a session is deleted on a guess.
func (s *Server) hasRow(ctx context.Context, id string) bool {
	if s.repo == nil {
		return false
	}
	rows, err := s.storedSessions(ctx)
	if err != nil {
		return true
	}
	for _, row := range rows {
		if row.ID == id {
			return true
		}
	}
	return false
}

// sessionLimits are the limits of the session key's scope: the defaults with
// the operator's override for that session on top.
func (o ResourceOptions) sessionLimits(key string) cgroup.Limits {
	return o.Limits.Merge(o.Overrides.Get(key))
}

// sessionTmpDir is the session key's scratch directory, or "" when sessions
// keep the host's TMPDIR or the key cannot name a directory.
func (o ResourceOptions) sessionTmpDir(key string) string {
	if o.TmpRoot == "" || !plainID(key) || key == "." || key == ".." {
		return ""
	}
	return filepath.Join(o.TmpRoot, key)
}

// tmpEnv returns env with the session's scratch directory in it, creating
// the directory. Values the creator set are kept.
//
// TMPDIR covers Codex, Pi, OpenCode and agy, which take the system temp
// directory from it. Claude Code builds its scratchpad and task-output
// directory as <base>/claude-<uid>/<cwd slug>/<session>/, where <base> is
// CLAUDE_CODE_TMPDIR when set, else TMPDIR on Linux, else /tmp; macOS
// ignores TMPDIR there, so both are set. Read from the 2.1.322 binary and
// checked by running it once with only TMPDIR and once with only
// CLAUDE_CODE_TMPDIR pointing at an empty directory: each run created
// claude-<uid>/ there and nothing under /tmp.
func (o ResourceOptions) tmpEnv(key string, env map[string]string) map[string]string {
	dir := o.sessionTmpDir(key)
	if dir == "" {
		return env
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("resources: session tmpdir %s: %v", dir, err)
		return env
	}
	out := make(map[string]string, len(env)+2)
	for k, v := range env {
		out[k] = v
	}
	for _, name := range []string{"TMPDIR", "CLAUDE_CODE_TMPDIR"} {
		if _, set := out[name]; !set {
			out[name] = dir
		}
	}
	return out
}

// forgetResources deletes what serve keeps for a session that is gone for
// good: its scratch directory and its limit override. A restart keeps both.
// Scratch files on a tmpfs stay charged as memory until they are deleted.
func (s *Server) forgetResources(id string) {
	o := s.resourceOptions()
	if dir := o.sessionTmpDir(id); dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("resources: remove %s: %v", dir, err)
		}
	}
	o.Overrides.Remove(id)
}
