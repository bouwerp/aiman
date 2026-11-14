//go:build pileup_e

package cigate_test

// Contract (client-pileup-guard P3b, piece E):
//
//   - const StatusQueued Status = "queued": the status Prepare writes. A run
//     turns StatusRunning when its turn comes.
//   - func (s Status) Finished() bool: true for StatusPass and StatusFail only.
//     aiman ci local --wait stops polling on a finished record (or a lost one)
//     and keeps polling while it is queued or running.
//   - Runner.MaxParallel is host.load.ci_parallel: runs at once per host, zero
//     means 2. Runs start in arrival order (the order of Prepare calls), FIFO.
//   - Runner.MayStart func() bool: nil means always. A queued run starts only
//     while it returns true (serve wires the pressure gate here).
//   - func (r *Runner) Kick(): re-checks the queue now; the gate calls it when
//     it opens. The runner also re-checks whenever a run ends.
//   - func (r *Runner) QueuePosition(session string) int: 1 is next; 0 when
//     the session has no queued run (running, finished or none).
//   - func (r *Runner) Drop(session string): takes the session's run out of
//     the queue (a running one is stopped as on a timeout), writes the record
//     as StatusFail with Error "session gone", returns that record from its
//     Wait and releases the session's claim and its slot. A dropped queued run
//     never starts. Serve's dropSession calls it before Forget.
//   - type Launcher func(unit string, cmd *exec.Cmd): puts one command in the
//     transient scope unit (systemd-run --user --scope --collect in
//     aiman-sessions.slice with the resources.session limits). The runner
//     calls it last, after Dir, Env and stdio are set; it rewrites Path and
//     Args only. Runner.Launch Launcher: nil runs the command plainly.
//   - Runner.Now func() time.Time: nil means time.Now; the unit's start time.
//   - Runner.TmpDir string: base of each run's TMPDIR (serve passes
//     ~/.aiman/tmp). A run's TMPDIR is TmpDir/ci-<session[:8]>-<sha[:12]>,
//     created before the run, set on the command's Env only (never on serve's
//     own environment) and removed when the run ends, pass or fail.
//   - func CIUnit(session, sha string, start time.Time) string:
//     "aiman-ci-<session>-<sha[:12]>-<start unix seconds>.scope", characters
//     systemd does not take in a unit name replaced by "_".
//   - func VerifyUnit(review string, start time.Time) string:
//     "aiman-verify-<review>-<start unix seconds>.scope", sanitised the same
//     way; verify's test command runs under it through the same Launcher.

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/cgroup"
	"github.com/bouwerp/aiman/internal/cigate"
)

// fakeLauncher stands in for systemd-run --scope. It records each unit and
// the command's environment, and models a transient scope: a unit name that
// is already live (a lock directory under dir) makes the command fail the
// way systemd-run does, and the name is free again once the command exits.
type fakeLauncher struct {
	t   *testing.T
	dir string

	mu    sync.Mutex
	units []string
	envs  [][]string
}

const fakeScopeScript = `lock=$1; shift
if ! mkdir "$lock" 2>/dev/null; then
  echo "Failed to start transient scope unit: Unit $(basename "$lock") was already loaded or has a fragment file." >&2
  exit 1
fi
"$@"; rc=$?
rmdir "$lock"
exit $rc`

func newFakeLauncher(t *testing.T) *fakeLauncher {
	t.Helper()
	return &fakeLauncher{t: t, dir: t.TempDir()}
}

func (f *fakeLauncher) launch(unit string, cmd *exec.Cmd) {
	f.mu.Lock()
	f.units = append(f.units, unit)
	f.envs = append(f.envs, slices.Clone(cmd.Env))
	f.mu.Unlock()
	sh, err := exec.LookPath("sh")
	if err != nil {
		f.t.Errorf("sh: %v", err)
		return
	}
	args := append([]string{"sh", "-c", fakeScopeScript, "sh", filepath.Join(f.dir, unit), cmd.Path}, cmd.Args[1:]...)
	cmd.Path, cmd.Args = sh, args
}

// leak marks unit as a live scope that outlasted its run.
func (f *fakeLauncher) leak(unit string) {
	f.t.Helper()
	if err := os.Mkdir(filepath.Join(f.dir, unit), 0o700); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeLauncher) calls() ([]string, [][]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.units), slices.Clone(f.envs)
}

// envValue is the value the command sees for key: the last entry wins.
func envValue(env []string, key string) (string, bool) {
	v, ok := "", false
	for _, kv := range env {
		if k, val, found := strings.Cut(kv, "="); found && k == key {
			v, ok = val, true
		}
	}
	return v, ok
}

var queueNow = time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)

// queueRig is a runner whose runs each hold until released. Every run's
// command appends its session to marks/order when it starts.
type queueRig struct {
	t     *testing.T
	r     *cigate.Runner
	marks string
	shas  map[string]string
}

func newQueueRig(t *testing.T, parallel int) *queueRig {
	t.Helper()
	marks := t.TempDir()
	return &queueRig{
		t:     t,
		marks: marks,
		shas:  map[string]string{},
		r: &cigate.Runner{
			Dir:         filepath.Join(t.TempDir(), "ci-local"),
			MaxParallel: parallel,
			TmpDir:      filepath.Join(t.TempDir(), "tmp"),
			Now:         func() time.Time { return queueNow },
		},
	}
}

// prepare makes a worktree for session whose list holds until released and
// prepares its run.
func (g *queueRig) prepare(session string) *cigate.Pending {
	g.t.Helper()
	run := "echo " + session + " >> " + filepath.Join(g.marks, "order") +
		"; while [ ! -f " + filepath.Join(g.marks, "release-"+session) + " ]; do sleep 0.02; done"
	dir := repo(g.t, "commands:\n  - name: hold\n    run: \""+run+"\"\n")
	g.shas[session] = git(g.t, dir, "rev-parse", "HEAD")
	p, err := g.r.Prepare(ctx(g.t), session, dir, cigate.Options{})
	if err != nil {
		g.t.Fatalf("Prepare %s: %v", session, err)
	}
	return p
}

func (g *queueRig) wait(p *cigate.Pending) <-chan cigate.Record {
	ch := make(chan cigate.Record, 1)
	go func() { ch <- p.Wait(ctx(g.t)) }()
	return ch
}

func (g *queueRig) release(session string) {
	g.t.Helper()
	write(g.t, g.marks, "release-"+session, "")
}

func (g *queueRig) started() []string {
	b, err := os.ReadFile(filepath.Join(g.marks, "order"))
	if err != nil {
		return nil
	}
	return strings.Fields(string(b))
}

func (g *queueRig) record(session string) cigate.Record {
	g.t.Helper()
	rec, ok, err := g.r.Record(session, g.shas[session])
	if err != nil || !ok {
		g.t.Fatalf("Record %s: ok=%v err=%v", session, ok, err)
	}
	return rec
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// settle gives a run that should not start a moment to (wrongly) start.
func settle() { time.Sleep(100 * time.Millisecond) }

func recv(t *testing.T, ch <-chan cigate.Record) cigate.Record {
	t.Helper()
	select {
	case rec := <-ch:
		return rec
	case <-time.After(20 * time.Second):
		t.Fatal("Wait did not return")
		return cigate.Record{}
	}
}

const (
	sA = "aaaaaaaa-0001"
	sB = "bbbbbbbb-0002"
	sC = "cccccccc-0003"
	sD = "dddddddd-0004"
)

func TestAcceptanceQueueStartsInArrivalOrderTwoAtATimeByDefault(t *testing.T) {
	g := newQueueRig(t, 0) // ci_parallel unset: 2
	sessions := []string{sA, sB, sC, sD}
	pending := map[string]*cigate.Pending{}
	for _, s := range sessions {
		p := g.prepare(s)
		if p.Record.Status != cigate.StatusQueued {
			t.Fatalf("Prepare %s wrote %q, want %q", s, p.Record.Status, cigate.StatusQueued)
		}
		pending[s] = p
	}
	// Waits are called in reverse: arrival is the order of Prepare.
	done := map[string]<-chan cigate.Record{}
	for _, s := range slices.Backward(sessions) {
		done[s] = g.wait(pending[s])
	}

	eventually(t, "two runs start", func() bool { return len(g.started()) >= 2 })
	settle()
	if got := g.started(); len(got) != 2 || !slices.Contains(got, sA) || !slices.Contains(got, sB) {
		t.Fatalf("started %v, want exactly the first two arrivals %s and %s", got, sA, sB)
	}
	for _, s := range []string{sA, sB} {
		if st := g.record(s).Status; st != cigate.StatusRunning {
			t.Fatalf("%s is %q, want running", s, st)
		}
	}
	for i, s := range []string{sC, sD} {
		if st := g.record(s).Status; st != cigate.StatusQueued {
			t.Fatalf("%s is %q, want queued", s, st)
		}
		if pos := g.r.QueuePosition(s); pos != i+1 {
			t.Fatalf("%s queue position %d, want %d", s, pos, i+1)
		}
	}
	if pos := g.r.QueuePosition(sA); pos != 0 {
		t.Fatalf("running %s has queue position %d, want 0", sA, pos)
	}

	g.release(sB)
	eventually(t, "the next arrival starts", func() bool { return len(g.started()) >= 3 })
	settle()
	if got := g.started(); len(got) != 3 || got[2] != sC {
		t.Fatalf("started %v, want %s third (FIFO), and no fourth while two run", got, sC)
	}
	if pos := g.r.QueuePosition(sD); pos != 1 {
		t.Fatalf("%s queue position %d, want 1", sD, pos)
	}

	for _, s := range []string{sA, sC, sD} {
		g.release(s)
	}
	for _, s := range sessions {
		if rec := recv(t, done[s]); rec.Status != cigate.StatusPass {
			t.Fatalf("%s finished %+v, want pass", s, rec)
		}
	}
	if got := g.started(); len(got) != 4 || got[3] != sD {
		t.Fatalf("started %v, want %s last", got, sD)
	}
}

func TestAcceptanceQueueRunsOneAtATimeWhenCIParallelIsOne(t *testing.T) {
	g := newQueueRig(t, 1)
	sessions := []string{sA, sB, sC}
	var done []<-chan cigate.Record
	for _, s := range sessions {
		done = append(done, g.wait(g.prepare(s)))
	}
	for i, s := range sessions {
		eventually(t, s+" starts", func() bool { return len(g.started()) >= i+1 })
		settle()
		if got := g.started(); len(got) != i+1 || got[i] != s {
			t.Fatalf("started %v, want %s alone as run %d", got, s, i+1)
		}
		for j, q := range sessions[i+1:] {
			if pos := g.r.QueuePosition(q); pos != j+1 {
				t.Fatalf("while %s runs, %s has position %d, want %d", s, q, pos, j+1)
			}
		}
		g.release(s)
		if rec := recv(t, done[i]); rec.Status != cigate.StatusPass {
			t.Fatalf("%s finished %+v, want pass", s, rec)
		}
	}
}

func TestAcceptanceQueueHoldsWhileTheGateIsClosed(t *testing.T) {
	g := newQueueRig(t, 0)
	var open atomic.Bool
	g.r.MayStart = open.Load
	doneA := g.wait(g.prepare(sA))
	doneB := g.wait(g.prepare(sB))
	settle()
	if got := g.started(); len(got) != 0 {
		t.Fatalf("started %v while the gate is closed, want nothing", got)
	}
	if st := g.record(sA).Status; st != cigate.StatusQueued {
		t.Fatalf("%s is %q while the gate is closed, want queued", sA, st)
	}
	if pos := g.r.QueuePosition(sA); pos != 1 {
		t.Fatalf("%s queue position %d, want 1", sA, pos)
	}
	g.r.Kick() // a kick with the gate still closed starts nothing
	settle()
	if got := g.started(); len(got) != 0 {
		t.Fatalf("started %v after a kick with the gate closed", got)
	}

	open.Store(true)
	g.r.Kick()
	eventually(t, "the head starts once the gate opens", func() bool { return len(g.started()) >= 1 })
	if got := g.started(); got[0] != sA {
		t.Fatalf("started %v, want the head %s first", got, sA)
	}
	g.release(sA)
	g.release(sB)
	for _, ch := range []<-chan cigate.Record{doneA, doneB} {
		if rec := recv(t, ch); rec.Status != cigate.StatusPass {
			t.Fatalf("finished %+v, want pass", rec)
		}
	}
}

// tmpRun runs one list for sA and returns the runner, the sha, the record and
// what the command saw as $TMPDIR.
func tmpRun(t *testing.T, launch cigate.Launcher, exit int) (*cigate.Runner, string, cigate.Record, string) {
	t.Helper()
	seen := filepath.Join(t.TempDir(), "seen")
	run := "printf %s \\\"$TMPDIR\\\" > " + seen + " && test -d \\\"$TMPDIR\\\" && touch \\\"$TMPDIR/build.o\\\" && exit " + strconv.Itoa(exit)
	dir := repo(t, "commands:\n  - name: build\n    run: \""+run+"\"\n")
	sha := git(t, dir, "rev-parse", "HEAD")
	r := &cigate.Runner{
		Dir:    filepath.Join(t.TempDir(), "ci-local"),
		TmpDir: filepath.Join(t.TempDir(), "tmp"),
		Now:    func() time.Time { return queueNow },
		Launch: launch,
	}
	rec, err := r.Run(ctx(t), sA, dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("the command did not run: %v (record %+v)", err, rec)
	}
	return r, sha, rec, string(b)
}

func TestAcceptanceRunGetsAUnitOfItsOwnAndATmpdirOnItsCommand(t *testing.T) {
	serveTmp, serveHas := os.LookupEnv("TMPDIR")
	for _, tc := range []struct {
		name string
		exit int
		want cigate.Status
	}{{"pass", 0, cigate.StatusPass}, {"fail", 3, cigate.StatusFail}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeLauncher(t)
			r, sha, rec, seen := tmpRun(t, f.launch, tc.exit)
			if rec.Status != tc.want {
				t.Fatalf("record %+v, want %s", rec, tc.want)
			}
			wantTmp := filepath.Join(r.TmpDir, "ci-"+sA[:8]+"-"+sha[:12])
			if seen != wantTmp {
				t.Fatalf("the command saw TMPDIR %q, want %q", seen, wantTmp)
			}
			units, envs := f.calls()
			if len(units) == 0 {
				t.Fatal("the launcher was never called")
			}
			wantUnit := "aiman-ci-" + sA + "-" + sha[:12] + "-" + strconv.FormatInt(queueNow.Unix(), 10) + ".scope"
			for i, u := range units {
				if u != wantUnit {
					t.Fatalf("unit %q, want %q", u, wantUnit)
				}
				if v, ok := envValue(envs[i], "TMPDIR"); !ok || v != wantTmp {
					t.Fatalf("launched env TMPDIR = %q (set %v), want %q", v, ok, wantTmp)
				}
			}
			if u := cigate.CIUnit(sA, sha, queueNow); u != wantUnit {
				t.Fatalf("CIUnit = %q, want %q", u, wantUnit)
			}
			if _, err := os.Stat(wantTmp); !os.IsNotExist(err) {
				t.Fatalf("the run's TMPDIR %s is still there after the run (%v)", wantTmp, err)
			}
			if got, has := os.LookupEnv("TMPDIR"); got != serveTmp || has != serveHas {
				t.Fatalf("serve's own TMPDIR changed to %q (set %v)", got, has)
			}
		})
	}
	t.Run("no user systemd", func(t *testing.T) {
		r, sha, rec, seen := tmpRun(t, nil, 0)
		wantTmp := filepath.Join(r.TmpDir, "ci-"+sA[:8]+"-"+sha[:12])
		if rec.Status != cigate.StatusPass || seen != wantTmp {
			t.Fatalf("record %+v, TMPDIR %q; want pass with TMPDIR %q", rec, seen, wantTmp)
		}
		if _, err := os.Stat(wantTmp); !os.IsNotExist(err) {
			t.Fatalf("the run's TMPDIR %s is still there after the run (%v)", wantTmp, err)
		}
	})
}

func TestAcceptanceLeftoverAndVerifyScopesDoNotBlockARun(t *testing.T) {
	const review = "rev12345-6789"
	wantVerify := "aiman-verify-" + review + "-" + strconv.FormatInt(queueNow.Unix(), 10) + ".scope"
	if u := cigate.VerifyUnit(review, queueNow); u != wantVerify {
		t.Fatalf("VerifyUnit = %q, want %q", u, wantVerify)
	}
	if u := cigate.CIUnit("a/b c", "0123456789abcdef", queueNow); strings.ContainsAny(u, "/ ") {
		t.Fatalf("CIUnit = %q keeps characters systemd refuses", u)
	}

	f := newFakeLauncher(t)
	dir := repo(t, "commands:\n  - name: c\n    run: \"true\"\n")
	sha := git(t, dir, "rev-parse", "HEAD")
	// Live while the run starts: the session's own scope (its agent), a CI
	// scope of the same session and commit leaked by an earlier run, and a
	// verify test command for a review of the same session.
	f.leak(cgroup.ScopeName(sA))
	f.leak(cigate.CIUnit(sA, sha, queueNow.Add(-time.Hour)))
	f.leak(wantVerify)

	r := &cigate.Runner{
		Dir:    filepath.Join(t.TempDir(), "ci-local"),
		TmpDir: filepath.Join(t.TempDir(), "tmp"),
		Now:    func() time.Time { return queueNow },
		Launch: f.launch,
	}
	rec, err := r.Run(ctx(t), sA, dir)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != cigate.StatusPass {
		t.Fatalf("record %+v, want pass beside the live scopes", rec)
	}
	units, _ := f.calls()
	if len(units) == 0 || units[0] != cigate.CIUnit(sA, sha, queueNow) {
		t.Fatalf("units %v, want the run's own %s", units, cigate.CIUnit(sA, sha, queueNow))
	}
}

func TestAcceptanceDroppedQueuedRunFailsSessionGoneAndNeverStarts(t *testing.T) {
	g := newQueueRig(t, 1)
	doneA := g.wait(g.prepare(sA))
	doneB := g.wait(g.prepare(sB))
	doneC := g.wait(g.prepare(sC))
	eventually(t, sA+" starts", func() bool { return len(g.started()) >= 1 })
	if pos := g.r.QueuePosition(sB); pos != 1 {
		t.Fatalf("%s queue position %d, want 1", sB, pos)
	}

	g.r.Drop(sB)
	rec := recv(t, doneB)
	if rec.Status != cigate.StatusFail || rec.Error != "session gone" {
		t.Fatalf("dropped run returned %+v, want fail with error %q", rec, "session gone")
	}
	if got := g.record(sB); got.Status != cigate.StatusFail || got.Error != "session gone" {
		t.Fatalf("dropped run's record %+v, want fail with error %q", got, "session gone")
	}
	if g.r.Running(sB) || g.r.QueuePosition(sB) != 0 {
		t.Fatalf("%s still holds its claim (running %v, position %d)", sB, g.r.Running(sB), g.r.QueuePosition(sB))
	}
	if pos := g.r.QueuePosition(sC); pos != 1 {
		t.Fatalf("%s queue position %d after the drop, want 1", sC, pos)
	}

	g.release(sA)
	g.release(sB)
	eventually(t, sC+" starts after the head", func() bool { return len(g.started()) >= 2 })
	g.release(sC)
	for _, ch := range []<-chan cigate.Record{doneA, doneC} {
		if rec := recv(t, ch); rec.Status != cigate.StatusPass {
			t.Fatalf("finished %+v, want pass", rec)
		}
	}
	if got := g.started(); slices.Contains(got, sB) {
		t.Fatalf("started %v: the dropped run started", got)
	}
	if err := g.r.Forget(sB); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptanceWaitKeepsPollingWhileQueued(t *testing.T) {
	for st, want := range map[cigate.Status]bool{
		cigate.StatusQueued:  false,
		cigate.StatusRunning: false,
		cigate.StatusPass:    true,
		cigate.StatusFail:    true,
	} {
		if got := st.Finished(); got != want {
			t.Errorf("%q.Finished() = %v, want %v", st, got, want)
		}
	}
}
