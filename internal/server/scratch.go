package server

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// scratchIdle is how long nothing inside a directory may change before
	// the janitor treats it as abandoned.
	scratchIdle = 24 * time.Hour
	// scratchFirstRun and scratchEvery are the janitor's schedule.
	scratchFirstRun = 10 * time.Minute
	scratchEvery    = time.Hour
)

// scratchPass is what the last janitor pass did, for host.stats.
type scratchPass struct {
	freed   int64
	lastRun time.Time
}

// ScratchReport is the host.stats "scratch" object.
type ScratchReport struct {
	FreedBytes  int64     `json:"freed_bytes"`
	LastRun     time.Time `json:"last_run"`
	LegacyBytes int64     `json:"legacy_bytes"`
}

// RunScratchJanitor runs SweepScratch 10 minutes after it is called and then
// every hour, until ctx ends.
func (s *Server) RunScratchJanitor(ctx context.Context) {
	timer := time.NewTimer(scratchFirstRun)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		s.SweepScratch(ctx)
		timer.Reset(scratchEvery)
	}
}

// SweepScratch is one janitor pass over ResourceOptions.TmpRoot. It removes a
// directory <TmpRoot>/<name> only when no stored session, running terminal or
// local CI run owns the name and nothing inside changed in the last 24 hours.
// Plain files and symlinks in the root stay, nothing is followed out of the
// root, and /tmp/claude-<uid> is never touched: a directory there may belong
// to a Claude process the operator runs by hand. It returns the bytes freed.
func (s *Server) SweepScratch(ctx context.Context) int64 {
	root := s.resourceOptions().TmpRoot
	if root == "" || s.repo == nil {
		return 0
	}
	known, ok := s.scratchOwners(ctx)
	var freed int64
	if ok {
		entries, err := os.ReadDir(root)
		if err != nil && !os.IsNotExist(err) {
			log.Printf("scratch: read %s: %v", root, err)
		}
		cutoff := time.Now().Add(-scratchIdle)
		for _, e := range entries {
			if ctx.Err() != nil {
				break
			}
			if !e.IsDir() || e.Type()&fs.ModeSymlink != 0 || !plainID(e.Name()) || ownsScratch(known, e.Name()) {
				continue
			}
			dir := filepath.Join(root, e.Name())
			size, newest := scratchTreeInfo(dir)
			if newest.After(cutoff) {
				continue
			}
			// A session saved since the first read keeps its directory.
			if fresh, ok := s.scratchOwners(ctx); !ok || ownsScratch(fresh, e.Name()) {
				continue
			}
			if err := os.RemoveAll(dir); err != nil {
				log.Printf("scratch: remove %s: %v", dir, err)
				continue
			}
			freed += size
		}
	}
	s.scratch.Store(&scratchPass{freed: freed, lastRun: time.Now()})
	log.Printf("scratch: janitor freed %d bytes under %s", freed, root)
	return freed
}

// scratchOwners lists the names that own scratch: every stored session and
// every pty. The rows are read from the repository, not the list cache, so a
// session saved a moment ago counts. ok is false when the read failed.
func (s *Server) scratchOwners(ctx context.Context) (map[string]bool, bool) {
	rows, err := s.repo.List(ctx)
	if err != nil {
		log.Printf("scratch: list sessions: %v", err)
		return nil, false
	}
	owners := make(map[string]bool, len(rows))
	for _, row := range rows {
		owners[row.ID] = true
	}
	if s.pty != nil {
		for _, info := range s.pty.List() {
			owners[info.ID] = true
		}
	}
	return owners, true
}

// ownsScratch reports whether name is an owner or a helper terminal of one
// ("<owner>-…", the scope key rule).
func ownsScratch(owners map[string]bool, name string) bool {
	if owners[name] || ciScratchName(name) {
		return true
	}
	for id := range owners {
		if strings.HasPrefix(name, id+"-") {
			return true
		}
	}
	return false
}

// ciScratchName keeps the janitor off the ci-local runs' scratch names, which start
// with "aiman-ci-" while a run is in progress.
func ciScratchName(name string) bool { return strings.HasPrefix(name, "aiman-ci-") }

// scratchTreeInfo returns the bytes of regular files under dir and the newest
// modification time of dir and everything in it. Symlinks are counted as
// themselves and never followed. An unreadable entry counts as modified now,
// so the directory stays.
func scratchTreeInfo(dir string) (int64, time.Time) {
	var size int64
	var newest time.Time
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			newest = time.Now()
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			newest = time.Now()
			return nil
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil // a link's own time says nothing about use
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		if info.Mode().IsRegular() {
			size += info.Size()
		}
		return nil
	})
	return size, newest
}

// dirBytes is the size of the regular files under dir.
func dirBytes(dir string) int64 {
	size, _ := scratchTreeInfo(dir)
	return size
}

// scratchReport fills the host.stats scratch object.
func (s *Server) scratchReport() ScratchReport {
	var r ScratchReport
	if st := s.scratch.Load(); st != nil {
		r.FreedBytes, r.LastRun = st.freed, st.lastRun
	}
	if legacy := s.resourceOptions().LegacyTmpRoot; legacy != "" {
		r.LegacyBytes = dirBytes(legacy)
	}
	return r
}

// forgetLegacyScratch removes <LegacyTmpRoot>/<any slug>/<agentSessionID>, the
// directory Claude Code used before sessions had a TMPDIR, and the slug
// directory that leaves empty.
func (o ResourceOptions) forgetLegacyScratch(agentSessionID string) {
	if o.LegacyTmpRoot == "" || !plainID(agentSessionID) || agentSessionID == "." || agentSessionID == ".." {
		return
	}
	slugs, err := os.ReadDir(o.LegacyTmpRoot)
	if err != nil {
		return
	}
	for _, slug := range slugs {
		if !slug.IsDir() || slug.Type()&fs.ModeSymlink != 0 {
			continue
		}
		slugDir := filepath.Join(o.LegacyTmpRoot, slug.Name())
		target := filepath.Join(slugDir, agentSessionID)
		if fi, err := os.Lstat(target); err != nil || !fi.IsDir() {
			continue
		}
		if err := os.RemoveAll(target); err != nil {
			log.Printf("resources: remove %s: %v", target, err)
			continue
		}
		_ = os.Remove(slugDir) // only succeeds when empty
	}
}
