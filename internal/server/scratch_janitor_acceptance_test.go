//go:build pileup_f

package server

// Contract:
//
//   - ResourceOptions.LegacyTmpRoot string: the directory Claude Code used for
//     scratch before sessions had a TMPDIR, /tmp/claude-<uid> in production.
//     Empty skips everything below that reads or removes it.
//   - func (s *Server) SweepScratch(ctx context.Context) int64: one janitor
//     pass over ResourceOptions.TmpRoot only, returning the bytes it freed. It
//     removes a directory <TmpRoot>/<id> only when <id> is no stored session
//     and no running pty and nothing inside was modified in the last 24 hours
//     (time.Now). It reads the stored sessions (SessionRepository.List) before
//     the walk and again immediately before each removal. It never removes a
//     plain file in TmpRoot, never follows a symlink and never reads or
//     removes anything under LegacyTmpRoot. The hourly loop that calls it is
//     not exercised here.
//   - forgetResources (reached through session.forget) also removes
//     <LegacyTmpRoot>/<any slug>/<row AgentSessionID> and a slug directory
//     that removal leaves empty.
//   - host.stats result gains "scratch": {"freed_bytes": int64 (the last
//     pass), "last_run": RFC 3339 time of the last pass, "legacy_bytes": int64
//     (the size of LegacyTmpRoot)}.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
)

const scratchFileSize = 5000

// listHookRepo runs onList once, after the first List call has read the rows,
// so a row it saves appears only to later reads.
type listHookRepo struct {
	domain.SessionRepository
	once   sync.Once
	onList func()
}

func (r *listHookRepo) List(ctx context.Context) ([]domain.Session, error) {
	rows, err := r.SessionRepository.List(ctx)
	if r.onList != nil {
		r.once.Do(r.onList)
	}
	return rows, err
}

type scratchFixture struct {
	srv    *Server
	pty    *ptyfake.FakePTY
	repo   *listHookRepo
	tmp    string
	legacy string
}

func scratchServer(t *testing.T, rows ...domain.Session) *scratchFixture {
	t.Helper()
	inner := testRepo(t)
	for i := range rows {
		if rows[i].Status == "" {
			rows[i].Status = domain.SessionStatusActive
		}
		if rows[i].CreatedAt.IsZero() {
			rows[i].CreatedAt = time.Now()
		}
		if err := inner.Save(context.Background(), &rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	repo := &listHookRepo{SessionRepository: inner}
	pty := ptyfake.New()
	srv := New(nil, repo, &fakeRemote{}, nil, nil, pty, "test")
	dir := t.TempDir()
	f := &scratchFixture{
		srv: srv, pty: pty, repo: repo,
		tmp:    filepath.Join(dir, "aiman-tmp"),
		legacy: filepath.Join(dir, "tmp", "claude-1004"),
	}
	for _, d := range []string{f.tmp, f.legacy} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	srv.SetResources(ResourceOptions{TmpRoot: f.tmp, LegacyTmpRoot: f.legacy})
	return f
}

// writeScratch writes a file of scratchFileSize bytes at path, creating its
// parents.
func writeScratch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", scratchFileSize)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// scratchAgeTree sets the mtime of root and everything under it, without following
// symlinks, to when.
func scratchAgeTree(t *testing.T, root string, when time.Time) {
	t.Helper()
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		return os.Chtimes(p, when, when)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func scratchExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func scratchOld() time.Time { return time.Now().Add(-72 * time.Hour) }

func scratchStats(t *testing.T, srv *Server) (freed, legacy int64, lastRun time.Time) {
	t.Helper()
	resp := rulesCall(t, srv, "", "host.stats", nil)
	if resp.Error != nil {
		t.Fatalf("host.stats: %+v", resp.Error)
	}
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Scratch *struct {
			FreedBytes  int64     `json:"freed_bytes"`
			LastRun     time.Time `json:"last_run"`
			LegacyBytes int64     `json:"legacy_bytes"`
		} `json:"scratch"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.Scratch == nil {
		t.Fatalf("host.stats has no scratch object (%v): %s", err, b)
	}
	return out.Scratch.FreedBytes, out.Scratch.LegacyBytes, out.Scratch.LastRun
}

// Old scratch of an id aiman does not know is removed from ~/.aiman/tmp; a
// directory under /tmp/claude-<uid> with an unknown id stays however old.
func TestAcceptanceScratchJanitorRemovesOldUnknownOnlyInAimanTmp(t *testing.T) {
	f := scratchServer(t)
	writeScratch(t, filepath.Join(f.tmp, "gone1", "claude-1004", "-w-x", "conv", "out.txt"))
	scratchAgeTree(t, filepath.Join(f.tmp, "gone1"), scratchOld())
	legacyDir := filepath.Join(f.legacy, "-w-stranger", "unknown-conv")
	writeScratch(t, filepath.Join(legacyDir, "tasks", "out.txt"))
	scratchAgeTree(t, f.legacy, time.Now().Add(-90*24*time.Hour))

	freed := f.srv.SweepScratch(context.Background())

	if scratchExists(filepath.Join(f.tmp, "gone1")) {
		t.Fatal("old scratch of an unknown id stayed in ~/.aiman/tmp")
	}
	if freed < scratchFileSize {
		t.Fatalf("freed %d bytes, want at least %d", freed, scratchFileSize)
	}
	if !scratchExists(filepath.Join(legacyDir, "tasks", "out.txt")) {
		t.Fatal("the janitor removed scratch under /tmp/claude-<uid>")
	}
}

// A running session's, a stored stopped session's and a recently modified
// directory stay.
func TestAcceptanceScratchJanitorKeepsLiveStoredAndRecent(t *testing.T) {
	stopped := rulesRow("stopped", "")
	stopped.Status = domain.SessionStatusInactive
	f := scratchServer(t, stopped)
	f.pty.Add(ptyruntime.SessionInfo{ID: "running", Status: ptyruntime.StatusRunning})

	for _, id := range []string{"running", "stopped", "fresh"} {
		writeScratch(t, filepath.Join(f.tmp, id, "a", "b", "out.txt"))
		scratchAgeTree(t, filepath.Join(f.tmp, id), scratchOld())
	}
	// Only a file deep inside is recent; the directory itself looks old.
	recent := filepath.Join(f.tmp, "fresh", "a", "b", "out.txt")
	if err := os.Chtimes(recent, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}

	f.srv.SweepScratch(context.Background())

	for _, id := range []string{"running", "stopped", "fresh"} {
		if !scratchExists(filepath.Join(f.tmp, id, "a", "b", "out.txt")) {
			t.Errorf("scratch of %q was removed", id)
		}
	}
}

// An id stored after the pass has read the sessions, while it walks, keeps
// its directory: the stored ids are read again before each removal.
func TestAcceptanceScratchJanitorKeepsIDStoredDuringWalk(t *testing.T) {
	f := scratchServer(t)
	for _, id := range []string{"late", "gone2"} {
		writeScratch(t, filepath.Join(f.tmp, id, "out.txt"))
		scratchAgeTree(t, filepath.Join(f.tmp, id), scratchOld())
	}
	inner := f.repo.SessionRepository
	f.repo.onList = func() {
		row := rulesRow("late", "")
		row.Status = domain.SessionStatusActive
		row.CreatedAt = time.Now()
		if err := inner.Save(context.Background(), &row); err != nil {
			t.Error(err)
		}
	}

	f.srv.SweepScratch(context.Background())

	if !scratchExists(filepath.Join(f.tmp, "late", "out.txt")) {
		t.Fatal("scratch of a session stored during the walk was removed")
	}
	if scratchExists(filepath.Join(f.tmp, "gone2")) {
		t.Fatal("old scratch of an unknown id stayed")
	}
}

// Files in ~/.aiman/tmp stay however old, and a symlink out of the root is
// not followed.
func TestAcceptanceScratchJanitorKeepsFilesAndDoesNotFollowSymlinks(t *testing.T) {
	f := scratchServer(t)
	outside := filepath.Join(filepath.Dir(f.tmp), "outside")
	writeScratch(t, filepath.Join(outside, "keep", "data.bin"))
	scratchAgeTree(t, outside, scratchOld())

	files := []string{"aiman-pty-x.json", "host-update-v1.2.3.sh"}
	for _, name := range files {
		writeScratch(t, filepath.Join(f.tmp, name))
	}
	// A root entry that is a symlink to a directory outside the root, and an
	// old unknown directory holding one.
	if err := os.Symlink(outside, filepath.Join(f.tmp, "linked")); err != nil {
		t.Fatal(err)
	}
	writeScratch(t, filepath.Join(f.tmp, "gone3", "out.txt"))
	if err := os.Symlink(filepath.Join(outside, "keep"), filepath.Join(f.tmp, "gone3", "escape")); err != nil {
		t.Fatal(err)
	}
	scratchAgeTree(t, f.tmp, scratchOld())

	f.srv.SweepScratch(context.Background())

	for _, name := range files {
		if !scratchExists(filepath.Join(f.tmp, name)) {
			t.Errorf("file %s in ~/.aiman/tmp was removed", name)
		}
	}
	if !scratchExists(filepath.Join(outside, "keep", "data.bin")) {
		t.Fatal("the janitor followed a symlink out of ~/.aiman/tmp and removed its target")
	}
	if scratchExists(filepath.Join(f.tmp, "gone3")) {
		t.Fatal("old unknown directory holding a symlink stayed")
	}
}

// Forgetting a session removes its legacy /tmp/claude-<uid> directory by
// AgentSessionID under any slug, and the slug directory that leaves empty.
func TestAcceptanceForgetRemovesLegacyClaudeScratchByAgentSessionID(t *testing.T) {
	row := rulesRow("a", "")
	row.AgentName = "claude"
	row.AgentSessionID = "conv-1"
	f := scratchServer(t, row)
	f.pty.Add(ptyruntime.SessionInfo{ID: "a", Status: ptyruntime.StatusRunning})

	mine := []string{
		filepath.Join(f.legacy, "-w-a", "conv-1", "tasks", "out.txt"),
		filepath.Join(f.legacy, "-elsewhere", "conv-1", "out.txt"),
	}
	for _, p := range mine {
		writeScratch(t, p)
	}
	other := filepath.Join(f.legacy, "-w-a", "conv-2", "out.txt")
	writeScratch(t, other)

	if resp := rulesCall(t, f.srv, "", "session.forget", map[string]any{"id": "a"}); resp.Error != nil {
		t.Fatalf("session.forget: %+v", resp.Error)
	}

	for _, slug := range []string{"-w-a", "-elsewhere"} {
		if scratchExists(filepath.Join(f.legacy, slug, "conv-1")) {
			t.Errorf("legacy scratch of conv-1 under %s stayed", slug)
		}
	}
	if !scratchExists(other) {
		t.Fatal("forget removed another conversation's legacy scratch")
	}
	if scratchExists(filepath.Join(f.legacy, "-elsewhere")) {
		t.Fatal("forget left the emptied slug directory")
	}
	if !scratchExists(f.legacy) {
		t.Fatal("forget removed /tmp/claude-<uid> itself")
	}
}

// host.stats reports the bytes the last pass freed, when it ran, and the size
// of /tmp/claude-<uid>.
func TestAcceptanceHostStatsReportsScratch(t *testing.T) {
	f := scratchServer(t)
	writeScratch(t, filepath.Join(f.tmp, "gone4", "a.txt"))
	writeScratch(t, filepath.Join(f.tmp, "gone4", "b.txt"))
	scratchAgeTree(t, filepath.Join(f.tmp, "gone4"), scratchOld())
	writeScratch(t, filepath.Join(f.legacy, "-w-x", "conv", "a.txt"))
	writeScratch(t, filepath.Join(f.legacy, "-w-y", "conv", "b.txt"))
	writeScratch(t, filepath.Join(f.legacy, "-w-y", "conv", "c.txt"))

	before := time.Now().Add(-time.Second)
	freed := f.srv.SweepScratch(context.Background())

	gotFreed, legacy, lastRun := scratchStats(t, f.srv)
	if gotFreed != freed || freed < 2*scratchFileSize {
		t.Errorf("freed_bytes %d, pass returned %d; want at least %d", gotFreed, freed, 2*scratchFileSize)
	}
	if lastRun.Before(before) || lastRun.After(time.Now().Add(time.Second)) {
		t.Errorf("last_run %v, want the pass just run", lastRun)
	}
	if legacy < 3*scratchFileSize {
		t.Errorf("legacy_bytes %d, want at least %d", legacy, 3*scratchFileSize)
	}
}
