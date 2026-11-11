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
)

var (
	// ErrDirty refuses a run while the worktree has tracked or untracked
	// changes: the record is keyed by commit, so only a commit can be checked.
	ErrDirty = errors.New("the worktree has uncommitted or untracked changes: commit first")
	// ErrRunning refuses a second run for a session.
	ErrRunning = errors.New("a local CI run is already running for this session")
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
	Session    string          `json:"session"`
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
}

// Runner runs gate lists and keeps their records under Dir/<session>/.
type Runner struct {
	Dir string
	// MaxParallel caps runs at once across sessions; more wait. Zero is 2.
	MaxParallel int
	// Copy is the default Options.Copy for Run.
	Copy []string

	mu      sync.Mutex
	running map[string]bool
	sem     chan struct{}
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

// Pending is a run that passed its checks and holds the session's slot. Wait
// must be called exactly once.
type Pending struct {
	r        *Runner
	session  string
	worktree string
	sha      string
	list     List
	opt      Options
	// Record is the running record already written.
	Record Record
}

// Prepare checks the worktree, reads the list and writes a running record. The
// commands run when Wait is called, so a caller can answer before they finish.
func (r *Runner) Prepare(ctx context.Context, session, worktree string, opt Options) (*Pending, error) {
	if err := checkName("session", session); err != nil {
		return nil, err
	}
	if !r.claim(session) {
		return nil, ErrRunning
	}
	p, err := r.prepare(ctx, session, worktree, opt)
	if err != nil {
		r.release(session)
		return nil, err
	}
	return p, nil
}

func (r *Runner) prepare(ctx context.Context, session, worktree string, opt Options) (*Pending, error) {
	status, err := gitOut(ctx, worktree, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(status) != "" {
		return nil, ErrDirty
	}
	sha, err := gitOut(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	sha = strings.TrimSpace(sha)
	list, ok, err := LoadList(ctx, worktree, sha, opt.Override)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNoList
	}
	rec := Record{Session: session, SHA: sha, Status: StatusRunning, Commands: []CommandResult{}, StartedAt: time.Now().UTC()}
	if err := r.write(rec); err != nil {
		return nil, err
	}
	return &Pending{r: r, session: session, worktree: worktree, sha: sha, list: list, opt: opt, Record: rec}, nil
}

// Wait queues for a host slot, runs the list and returns the final record.
func (p *Pending) Wait(ctx context.Context) Record {
	r := p.r
	defer r.release(p.session)
	rec := p.Record
	if err := r.acquire(ctx); err != nil {
		rec = finish(rec, StatusFail, "", "cancelled while queued: "+err.Error())
	} else {
		rec = p.run(ctx, rec)
		<-r.sem
	}
	_ = r.write(rec)
	r.prune(p.session, p.sha)
	return rec
}

func finish(rec Record, st Status, failed, msg string) Record {
	rec.Status, rec.Failed, rec.Error = st, failed, msg
	rec.FinishedAt = time.Now().UTC()
	return rec
}

func (p *Pending) run(ctx context.Context, rec Record) Record {
	snap, err := os.MkdirTemp(p.r.sessionDir(p.session), ".snapshot-")
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
	steps := p.list.Commands
	if p.list.Setup != "" {
		steps = append([]Command{{Name: "setup", Run: p.list.Setup, Timeout: DefaultTimeout}}, steps...)
	}
	for i, c := range steps {
		res := p.runOne(ctx, snap, i, c)
		rec.Commands = append(rec.Commands, res)
		_ = p.r.write(rec)
		if res.Exit != 0 {
			return finish(rec, StatusFail, c.Name, "")
		}
	}
	return finish(rec, StatusPass, "", "")
}

func (p *Pending) runOne(ctx context.Context, snap string, i int, c Command) CommandResult {
	logPath := filepath.Join(p.r.sessionDir(p.session), fmt.Sprintf("%s.%d-%s.log", p.sha, i, logSafe(c.Name)))
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
	cmd.Env = append(os.Environ(), "CI=true", "AIMAN_CI_LOCAL=1")
	err = cmd.Run()
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

func (r *Runner) claim(session string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running == nil {
		r.running = map[string]bool{}
	}
	if r.running[session] {
		return false
	}
	r.running[session] = true
	return true
}

func (r *Runner) release(session string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.running, session)
}

// Running reports whether a run for session is in progress in this runner.
func (r *Runner) Running(session string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running[session]
}

func (r *Runner) acquire(ctx context.Context) error {
	r.mu.Lock()
	if r.sem == nil {
		n := r.MaxParallel
		if n <= 0 {
			n = 2
		}
		r.sem = make(chan struct{}, n)
	}
	sem := r.sem
	r.mu.Unlock()
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) sessionDir(session string) string { return filepath.Join(r.Dir, session) }

func (r *Runner) write(rec Record) error {
	dir := r.sessionDir(rec.Session)
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

// Record reads the record for session and sha. A running record whose run is
// not in progress in this runner (serve restarted mid-run) reads as failed.
func (r *Runner) Record(session, sha string) (Record, bool, error) {
	if err := checkName("session", session); err != nil {
		return Record{}, false, err
	}
	if err := checkName("sha", sha); err != nil {
		return Record{}, false, err
	}
	rec, err := readRecord(filepath.Join(r.sessionDir(session), sha+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	if rec.Status == StatusRunning && !r.Running(session) {
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
func (r *Runner) prune(session, head string) {
	dir := r.sessionDir(session)
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
