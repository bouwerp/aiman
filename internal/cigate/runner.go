package cigate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Status is a gate record's outcome.
type Status string

const (
	StatusPass    Status = "pass"
	StatusFail    Status = "fail"
	StatusRunning Status = "running"
	// StatusQueued is a run waiting for a host slot or for the pressure gate.
	StatusQueued Status = "queued"
)

// Finished reports whether the status is an outcome: pass or fail. A queued
// or running record is still in progress.
func (s Status) Finished() bool { return s == StatusPass || s == StatusFail }

var (
	// ErrDirty refuses a run while the worktree has tracked or untracked
	// changes: the record is keyed by commit, so only a commit can be checked.
	ErrDirty = errors.New("the worktree has uncommitted or untracked changes: commit first")
	// ErrRunning refuses a second run for a session, queued or running.
	ErrRunning = errors.New("a local CI run is already queued or running for this session")
	// ErrNoList means the commit has no gate list and no override applies.
	ErrNoList = errors.New("this repository has no local CI list (" + ListPath + " or repos.<slug>.ci.local)")
)

// keepRecords is how many records a session keeps besides HEAD's.
const keepRecords = 5

// CommandResult is one command's outcome. Log is the absolute path of its
// output.
type CommandResult struct {
	Name     string `json:"name"`
	Exit     int    `json:"exit"`
	Log      string `json:"log"`
	TimedOut bool   `json:"timed_out,omitempty"`
	Millis   int64  `json:"duration_ms"`
}

// Record is the result of one gate run for one session and commit.
type Record struct {
	Session string `json:"session"`
	// Repo is the member's owner/name slug for a run of a workspace session;
	// empty for a single-repo session.
	Repo       string          `json:"repo,omitempty"`
	SHA        string          `json:"sha"`
	Status     Status          `json:"status"`
	Failed     string          `json:"failed,omitempty"`
	Error      string          `json:"error,omitempty"`
	Commands   []CommandResult `json:"commands"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt time.Time       `json:"finished_at,omitzero"`
}

// Options are per-run settings that depend on the repository.
type Options struct {
	// Copy lists repo-relative files copied from the worktree into the
	// snapshot (repos.<slug>.copy). Nothing else is copied or linked.
	Copy []string
	// Override replaces the committed list (repos.<slug>.ci.local).
	Override *List
	// Nice is the nice increment for the commands; 0 runs them unchanged.
	Nice int
	// Repo names the member of a workspace session the run is for. Runs,
	// claims, records and units are keyed by session and Repo; empty keeps
	// the single-repo layout Dir/<session>/.
	Repo string
}

// RepoKey is the directory and unit name of a member repository:
// owner__name in lower case.
func RepoKey(repo string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(repo), "/", "__"))
}

// runKey names one claim: the session, plus the member for a workspace run.
func runKey(session, repo string) string {
	if repo == "" {
		return session
	}
	return session + "/" + RepoKey(repo)
}

// Launcher puts one command in the transient scope unit. The runner calls it
// last, after Dir, Env and stdio are set; it rewrites Path and Args only.
type Launcher func(unit string, cmd *exec.Cmd)

// Runner runs gate lists and keeps their records under Dir/<session>/, or
// Dir/<session>/<repo key>/ for a member of a workspace session. Runs
// wait in one FIFO queue, in the order of Prepare calls.
type Runner struct {
	Dir string
	// MaxParallel caps runs at once across sessions (host.load.ci_parallel);
	// more wait in the queue. Zero is 2.
	MaxParallel int
	// Copy is the default Options.Copy for Run.
	Copy []string
	// MayStart is asked before a queued run starts; nil means always. Serve
	// wires the pressure gate here and calls Kick when it opens.
	MayStart func() bool
	// Launch puts each command in a scope unit of its own run; nil runs the
	// command plainly.
	Launch Launcher
	// Now is the clock for the unit's start time; nil means time.Now.
	Now func() time.Time
	// TmpDir is the base of each run's TMPDIR, TmpDir/ci-<session8>-<sha12>;
	// empty leaves TMPDIR alone.
	TmpDir string

	mu     sync.Mutex
	claims map[string]*Pending
	queue  []*Pending
	active int
	// starting is the run released last until it has spawned its first
	// command, so runs start in arrival order even when two slots are free.
	starting *Pending
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func checkName(kind, v string) error {
	if !safeName.MatchString(v) || v == "." || v == ".." {
		return fmt.Errorf("invalid %s %q", kind, v)
	}
	return nil
}

// Run runs the gate for the worktree's HEAD and waits for the result.
func (r *Runner) Run(ctx context.Context, session, worktree string) (Record, error) {
	return r.RunWith(ctx, session, worktree, Options{Copy: r.Copy})
}

// RunWith is Run with explicit options.
func (r *Runner) RunWith(ctx context.Context, session, worktree string, opt Options) (Record, error) {
	p, err := r.Prepare(ctx, session, worktree, opt)
	if err != nil {
		return Record{}, err
	}
	return p.Wait(ctx), nil
}

// Pending is a run that passed its checks and holds the session's claim.
// Wait must be called exactly once.
type Pending struct {
	r        *Runner
	session  string
	repo     string
	worktree string
	sha      string
	list     List
	opt      Options
	// Record is the queued record Prepare wrote.
	Record Record

	// Guarded by r.mu.
	state   pendingState
	rec     Record // the latest record written
	gone    bool   // Drop was called
	cancel  context.CancelFunc
	started chan struct{} // closed when the run gets its turn
	dropped chan struct{} // closed when Drop takes it out of the queue
	done    chan struct{} // closed when Wait has finished
}

type pendingState int

const (
	pendingQueued pendingState = iota
	pendingRunning
	pendingDone
)

// Prepare checks the worktree, reads the list, writes a queued record and
// puts the run at the back of the queue. The commands run when its turn
// comes and Wait is called, so a caller can answer before they finish.
func (r *Runner) Prepare(ctx context.Context, session, worktree string, opt Options) (*Pending, error) {
	if err := checkName("session", session); err != nil {
		return nil, err
	}
	if err := checkRepo(opt.Repo); err != nil {
		return nil, err
	}
	p := &Pending{r: r, session: session, repo: opt.Repo, worktree: worktree, opt: opt,
		started: make(chan struct{}), dropped: make(chan struct{}), done: make(chan struct{})}
	if !r.claim(p) {
		return nil, ErrRunning
	}
	if err := r.prepare(ctx, p); err != nil {
		r.release(p)
		return nil, err
	}
	r.mu.Lock()
	r.queue = append(r.queue, p)
	r.mu.Unlock()
	r.Kick()
	return p, nil
}

func (r *Runner) prepare(ctx context.Context, p *Pending) error {
	status, err := gitOut(ctx, p.worktree, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return ErrDirty
	}
	sha, err := gitOut(ctx, p.worktree, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	sha = strings.TrimSpace(sha)
	list, ok, err := LoadList(ctx, p.worktree, sha, p.opt.Override)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNoList
	}
	rec := Record{Session: p.session, Repo: p.repo, SHA: sha, Status: StatusQueued, Commands: []CommandResult{}, StartedAt: time.Now().UTC()}
	if err := r.write(rec); err != nil {
		return err
	}
	p.sha, p.list, p.Record, p.rec = sha, list, rec, rec
	return nil
}

// Kick starts queued runs, head first, while a slot is free and MayStart
// allows. The runner kicks itself on every Prepare and whenever a run ends.
func (r *Runner) Kick() {
	may := r.MayStart == nil || r.MayStart()
	r.mu.Lock()
	defer r.mu.Unlock()
	for may && r.starting == nil && r.active < r.maxParallel() && len(r.queue) > 0 {
		p := r.queue[0]
		r.queue = r.queue[1:]
		r.active++
		r.starting = p
		p.state = pendingRunning
		p.rec.Status = StatusRunning
		_ = r.write(p.rec)
		close(p.started)
	}
}

func (r *Runner) maxParallel() int {
	if r.MaxParallel <= 0 {
		return 2
	}
	return r.MaxParallel
}

// QueuePosition is the session's place in the queue, 1 next; 0 when it has
// no queued run.
func (r *Runner) QueuePosition(session string) int {
	return r.QueuePositionFor(session, "")
}

// QueuePositionFor is QueuePosition for one member of a workspace session.
func (r *Runner) QueuePositionFor(session, repo string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := runKey(session, repo)
	for i, p := range r.queue {
		if p.key() == key {
			return i + 1
		}
	}
	return 0
}

// dropWait bounds how long Drop waits for a stopped run to wind down.
const dropWait = 30 * time.Second

// Drop ends the session's run because the session is gone: a queued run
// leaves the queue and never starts, a running one is stopped as on a
// timeout. Either way the record is failed with "session gone", Wait returns
// it, and the session's claim and slot are released.
func (r *Runner) Drop(session string) {
	r.mu.Lock()
	var mine []*Pending
	for _, p := range r.claims {
		if p.session == session {
			mine = append(mine, p)
		}
	}
	r.mu.Unlock()
	for _, p := range mine {
		r.drop(p)
	}
}

// drop ends one claimed run as Drop describes.
func (r *Runner) drop(p *Pending) {
	r.mu.Lock()
	if r.claims[p.key()] != p {
		r.mu.Unlock()
		return
	}
	p.gone = true
	switch p.state {
	case pendingQueued:
		r.queue = slices.DeleteFunc(r.queue, func(q *Pending) bool { return q == p })
		p.state = pendingDone
		p.rec = finish(p.rec, StatusFail, "", "session gone")
		_ = r.write(p.rec)
		delete(r.claims, p.key())
		close(p.dropped)
		r.mu.Unlock()
		r.Kick()
		return
	case pendingRunning:
		if p.cancel != nil {
			p.cancel()
		}
	}
	r.mu.Unlock()
	// A run whose Wait has not reached its commands sees gone and skips them.
	select {
	case <-p.done:
	case <-time.After(dropWait):
	}
}

// Wait waits for the run's turn, runs the list and returns the final record.
// A run dropped while queued returns its failed record without running.
func (p *Pending) Wait(ctx context.Context) Record {
	r := p.r
	defer close(p.done)
	select {
	case <-p.started:
	case <-p.dropped:
		return p.final()
	case <-ctx.Done():
		r.mu.Lock()
		if p.state == pendingQueued {
			r.queue = slices.DeleteFunc(r.queue, func(q *Pending) bool { return q == p })
			p.state = pendingDone
			p.rec = finish(p.rec, StatusFail, "", "cancelled while queued: "+ctx.Err().Error())
			_ = r.write(p.rec)
			if r.claims[p.key()] == p {
				delete(r.claims, p.key())
			}
			r.mu.Unlock()
			r.Kick()
			return p.final()
		}
		r.mu.Unlock()
		select {
		case <-p.started:
		case <-p.dropped:
			return p.final()
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.mu.Lock()
	p.cancel = cancel
	rec, gone := p.rec, p.gone
	r.mu.Unlock()
	if !gone {
		rec = p.run(runCtx, rec)
	}
	p.launched()

	r.mu.Lock()
	if p.gone {
		rec = finish(rec, StatusFail, rec.Failed, "session gone")
	}
	p.state, p.rec = pendingDone, rec
	_ = r.write(rec)
	r.active--
	if r.claims[p.key()] == p {
		delete(r.claims, p.key())
	}
	r.mu.Unlock()
	r.prune(p.dir(), p.sha)
	r.Kick()
	return rec
}

// launched lets the next queued run start: this one has spawned its first
// command, or will not.
func (p *Pending) launched() {
	r := p.r
	r.mu.Lock()
	was := r.starting == p
	if was {
		r.starting = nil
	}
	r.mu.Unlock()
	if was {
		r.Kick()
	}
}

func (p *Pending) final() Record {
	p.r.mu.Lock()
	defer p.r.mu.Unlock()
	return p.rec
}

func finish(rec Record, st Status, failed, msg string) Record {
	rec.Status, rec.Failed, rec.Error = st, failed, msg
	rec.FinishedAt = time.Now().UTC()
	return rec
}

func (p *Pending) run(ctx context.Context, rec Record) Record {
	if err := os.MkdirAll(p.dir(), 0o700); err != nil {
		return finish(rec, StatusFail, "", err.Error())
	}
	snap, err := os.MkdirTemp(p.dir(), ".snapshot-")
	if err != nil {
		return finish(rec, StatusFail, "", err.Error())
	}
	// git worktree add wants a path that does not exist yet.
	_ = os.Remove(snap)
	defer removeSnapshot(p.worktree, snap)
	if _, err := gitOut(ctx, p.worktree, "worktree", "add", "--detach", snap, p.sha); err != nil {
		return finish(rec, StatusFail, "", "snapshot: "+err.Error())
	}
	if err := copyFiles(p.worktree, snap, p.opt.Copy); err != nil {
		return finish(rec, StatusFail, "", "copy: "+err.Error())
	}
	env := append(os.Environ(), "CI=true", "AIMAN_CI_LOCAL=1")
	if p.r.TmpDir != "" {
		name := "ci-" + prefix(p.session, 8) + "-" + prefix(p.sha, 12)
		if p.repo != "" {
			name = "ci-" + prefix(p.session, 8) + "-" + RepoKey(p.repo) + "-" + prefix(p.sha, 12)
		}
		tmp, release, err := ClaimTmp(p.r.TmpDir, name)
		if err != nil {
			return finish(rec, StatusFail, "", "tmpdir: "+err.Error())
		}
		defer release()
		env = append(env, "TMPDIR="+tmp)
	}
	unit := CIUnitFor(p.session, p.repo, p.sha, p.r.now())
	steps := p.list.Commands
	if p.list.Setup != "" {
		steps = append([]Command{{Name: "setup", Run: p.list.Setup, Timeout: DefaultTimeout}}, steps...)
	}
	for i, c := range steps {
		res := p.runOne(ctx, snap, unit, env, i, c)
		rec.Commands = append(rec.Commands, res)
		_ = p.r.write(rec)
		if res.Exit != 0 {
			return finish(rec, StatusFail, c.Name, "")
		}
	}
	return finish(rec, StatusPass, "", "")
}

func (p *Pending) runOne(ctx context.Context, snap, unit string, env []string, i int, c Command) CommandResult {
	logPath := filepath.Join(p.dir(), fmt.Sprintf("%s.%d-%s.log", p.sha, i, logSafe(c.Name)))
	res := CommandResult{Name: c.Name, Log: logPath, Exit: -1}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return res
	}
	defer func() { _ = f.Close() }()
	cctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	start := time.Now()
	cmd := shellCommand(cctx, p.opt.Nice, c.Run)
	cmd.Dir = filepath.Join(snap, c.Dir)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.Stdin = nil
	cmd.Env = env
	if p.r.Launch != nil {
		p.r.Launch(unit, cmd)
	}
	err = cmd.Start()
	p.launched()
	if err == nil {
		err = cmd.Wait()
	}
	res.Millis = time.Since(start).Milliseconds()
	res.Exit = exitCode(err)
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
		_, _ = fmt.Fprintf(f, "\naiman: %s timed out after %s\n", c.Name, c.Timeout)
		if res.Exit == 0 {
			res.Exit = -1
		}
	} else if err != nil && res.Exit == -1 {
		_, _ = fmt.Fprintf(f, "\naiman: %v\n", err)
	}
	return res
}

func shellCommand(ctx context.Context, nice int, script string) *exec.Cmd {
	var cmd *exec.Cmd
	if nice > 0 {
		cmd = exec.CommandContext(ctx, "nice", "-n", strconv.Itoa(min(nice, 19)), "bash", "-lc", script) //nolint:gosec // G204: the gate list is the repository's own command list
	} else {
		cmd = exec.CommandContext(ctx, "bash", "-lc", script) //nolint:gosec // G204: the gate list is the repository's own command list
	}
	setProcessGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() >= 0 {
		return ee.ExitCode()
	}
	return -1
}

func logSafe(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r == '-' || r == '_' || r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func removeSnapshot(worktree, snap string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, _ = gitOut(ctx, worktree, "worktree", "remove", "--force", snap)
	_ = os.RemoveAll(snap)
	_, _ = gitOut(ctx, worktree, "worktree", "prune")
}

// copyFiles copies each repo-relative regular file or directory from src to
// dst. Missing entries and symlinks are skipped; nothing is linked.
func copyFiles(src, dst string, paths []string) error {
	for _, p := range paths {
		if err := ValidateDir(p); err != nil || filepath.Clean(p) == "." {
			continue
		}
		from := filepath.Join(src, p)
		info, err := os.Lstat(from)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if err := copyTree(from, filepath.Join(dst, p)); err != nil {
			return err
		}
	}
	return nil
}

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		target := filepath.Join(to, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o750)
		case d.Type().IsRegular():
			return copyFile(path, target)
		default:
			return nil
		}
	})
}

func copyFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o750); err != nil {
		return err
	}
	in, err := os.Open(from) //nolint:gosec // G304: a repos.<slug>.copy path inside the worktree
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm()) //nolint:gosec // G304: inside the snapshot
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func (r *Runner) claim(p *Pending) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claims == nil {
		r.claims = map[string]*Pending{}
	}
	if r.claims[p.key()] != nil {
		return false
	}
	r.claims[p.key()] = p
	return true
}

func (r *Runner) release(p *Pending) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claims[p.key()] == p {
		delete(r.claims, p.key())
	}
}

// Running reports whether a run for session is queued or running in this
// runner.
func (r *Runner) Running(session string) bool { return r.RunningFor(session, "") }

// RunningFor is Running for one member of a workspace session.
func (r *Runner) RunningFor(session, repo string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.claims[runKey(session, repo)] != nil
}

func (p *Pending) key() string { return runKey(p.session, p.repo) }

// dir is where the run's record, logs and snapshot live.
func (p *Pending) dir() string { return p.r.runDir(p.session, p.repo) }

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) sessionDir(session string) string { return filepath.Join(r.Dir, session) }

// runDir is the session's directory, or its member's below it.
func (r *Runner) runDir(session, repo string) string {
	if repo == "" {
		return r.sessionDir(session)
	}
	return filepath.Join(r.sessionDir(session), RepoKey(repo))
}

// checkRepo accepts an empty repo or an owner/name slug whose key is a safe
// name.
func checkRepo(repo string) error {
	if repo == "" {
		return nil
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || checkName("repo", owner) != nil || checkName("repo", name) != nil {
		return fmt.Errorf("invalid repo %q", repo)
	}
	return nil
}

func (r *Runner) write(rec Record) error {
	dir := r.runDir(rec.Session, rec.Repo)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".record-")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, rec.SHA+".json"))
}

// Record reads the record for session and sha. A queued or running record
// whose run is not in this runner (serve restarted) reads as failed.
func (r *Runner) Record(session, sha string) (Record, bool, error) {
	return r.RecordFor(session, "", sha)
}

// RecordFor is Record for one member of a workspace session.
func (r *Runner) RecordFor(session, repo, sha string) (Record, bool, error) {
	if err := checkName("session", session); err != nil {
		return Record{}, false, err
	}
	if err := checkRepo(repo); err != nil {
		return Record{}, false, err
	}
	if err := checkName("sha", sha); err != nil {
		return Record{}, false, err
	}
	rec, err := readRecord(filepath.Join(r.runDir(session, repo), sha+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	if !rec.Status.Finished() && !r.RunningFor(session, repo) {
		rec.Status = StatusFail
		rec.Error = "the run was interrupted (serve restarted); run aiman ci local again"
	}
	return rec, true, nil
}

func readRecord(path string) (Record, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: path is built from validated names under Dir
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal(b, &rec); err != nil {
		return Record{}, fmt.Errorf("record %s: %w", filepath.Base(path), err)
	}
	return rec, nil
}

// prune keeps the newest keepRecords records plus head's and removes the rest
// with their logs.
func (r *Runner) prune(dir, head string) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	type entry struct {
		sha string
		at  time.Time
	}
	var all []entry
	for _, f := range files {
		sha := strings.TrimSuffix(filepath.Base(f), ".json")
		if sha == head {
			continue
		}
		rec, err := readRecord(f)
		if err != nil {
			continue
		}
		all = append(all, entry{sha, rec.StartedAt})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.After(all[j].at) })
	if len(all) <= keepRecords {
		return
	}
	for _, e := range all[keepRecords:] {
		_ = os.Remove(filepath.Join(dir, e.sha+".json"))
		logs, _ := filepath.Glob(filepath.Join(dir, e.sha+".*.log"))
		for _, l := range logs {
			_ = os.Remove(l)
		}
	}
}

// Forget removes every record and log of session.
func (r *Runner) Forget(session string) error {
	if err := checkName("session", session); err != nil {
		return err
	}
	return os.RemoveAll(r.sessionDir(session))
}

// Tail returns the last n lines of a command log.
func Tail(path string, n int) (string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: a log path from a gate record
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}
