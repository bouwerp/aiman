package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// Run states.
const (
	StateStarting = "starting"
	StateReady    = "ready"
	StateFailed   = "failed"
	StateStopped  = "stopped"
)

// Stop reasons recorded in harness.stopped.
const (
	StopDown         = "down"
	StopIdle         = "idle"
	StopSessionGone  = "session_gone"
	StopServeRestart = "serve_restart"
)

// Verdict results.
const (
	ResultPass = "pass"
	ResultFail = "fail"
	ResultSkip = "skip"
)

// KeepRuns is how many run directories serve keeps per session.
const KeepRuns = 5

// Run is one start of one scenario at one commit for one session. It is the
// run.json of the run directory and the run object of the RPCs.
type Run struct {
	ID         string `json:"id"`
	Session    string `json:"session"`
	Scenario   string `json:"scenario"`
	Title      string `json:"title,omitempty"`
	SHA        string `json:"sha"`
	Dirty      bool   `json:"dirty"`
	DiffSHA256 string `json:"diff_sha256,omitempty"`
	Source     Source `json:"source"`
	Network    string `json:"network"`
	State      string `json:"state"`
	Debug      bool   `json:"debug"`
	// DebugLaunch is the DAP launch request of a debug run.
	DebugLaunch *DebugLaunch `json:"debug_launch,omitempty"`
	// FailedStep, FailedService and Error explain a failed run.
	FailedStep    string     `json:"failed_step,omitempty"`
	FailedService string     `json:"failed_service,omitempty"`
	Error         string     `json:"error,omitempty"`
	Ports         []RunPort  `json:"ports"`
	Steps         []StepView `json:"steps"`
	Checks        []CheckRun `json:"checks"`
	// Result is the tester's verdict on the whole scenario.
	Result   string    `json:"result,omitempty"`
	Verdicts []Verdict `json:"verdicts,omitempty"`
	// Services are the scenario's service names; Images maps each container
	// to the image ID it runs.
	Services    []string          `json:"services,omitempty"`
	Images      map[string]string `json:"images,omitempty"`
	InnerSubnet string            `json:"inner_subnet,omitempty"`
	OuterSubnet string            `json:"outer_subnet,omitempty"`
	StartedAt   time.Time         `json:"started_at"`
	ReadyAt     *time.Time        `json:"ready_at,omitempty"`
	StoppedAt   *time.Time        `json:"stopped_at,omitempty"`
	StopReason  string            `json:"stop_reason,omitempty"`
	// LastUsed is the last RPC that touched the run; harness.idle_timeout
	// counts from it.
	LastUsed time.Time `json:"last_used"`
	// BrowserSourcemaps is set shortly after the run is ready (P6e).
	BrowserSourcemaps *BrowserSourcemaps `json:"browser_sourcemaps,omitempty"`
}

// Live reports whether the run holds containers.
func (r Run) Live() bool { return r.State == StateStarting || r.State == StateReady }

// RunPort is a relayed port: the edge publishes it on host 127.0.0.1:HostPort.
type RunPort struct {
	Name     string `json:"name"`
	Service  string `json:"service"`
	HostPort int    `json:"host_port"`
}

// StepView is one human step with its latest verdict. N counts from 1.
type StepView struct {
	N      int    `json:"n"`
	Do     string `json:"do"`
	Expect string `json:"expect,omitempty"`
	Result string `json:"result,omitempty"`
	Note   string `json:"note,omitempty"`
}

// CheckRun is one scenario check and its latest result; Exit is nil until it
// ran. Output is the redacted tail of its output.
type CheckRun struct {
	Name   string     `json:"name"`
	Run    string     `json:"run"`
	Exit   *int       `json:"exit,omitempty"`
	At     *time.Time `json:"at,omitempty"`
	Output string     `json:"output,omitempty"`
}

// Verdict is a tester's pass, fail or skip for a step (0 for the scenario).
// SHA, Dirty and DiffSHA256 name the tree it judged.
type Verdict struct {
	RunID      string    `json:"run"`
	Step       int       `json:"step"`
	Result     string    `json:"result"`
	Note       string    `json:"note,omitempty"`
	By         string    `json:"by,omitempty"`
	SHA        string    `json:"sha"`
	Dirty      bool      `json:"dirty"`
	DiffSHA256 string    `json:"diff_sha256,omitempty"`
	At         time.Time `json:"at"`
}

// Result is result.json: the verdicts and checks of a run.
type Result struct {
	Run        string     `json:"run"`
	Scenario   string     `json:"scenario"`
	SHA        string     `json:"sha"`
	Dirty      bool       `json:"dirty"`
	DiffSHA256 string     `json:"diff_sha256,omitempty"`
	Source     Source     `json:"source"`
	Result     string     `json:"result,omitempty"`
	Steps      []StepView `json:"steps"`
	Checks     []CheckRun `json:"checks"`
	Verdicts   []Verdict  `json:"verdicts,omitempty"`
}

var safeIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// runDir is <Dir>/<session>/<run>.
// RunDir is the directory of a run: src/, sandbox/ (compose.yaml), logs/,
// run.json and result.json.
func (m *Manager) RunDir(session, id string) string { return m.runDir(session, id) }

func (m *Manager) runDir(session, id string) string {
	return filepath.Join(m.opts.Dir, session, id)
}

func (m *Manager) writeRun(r Run) error {
	dir := m.runDir(r.Session, r.ID)
	if err := writeJSON(filepath.Join(dir, "run.json"), r); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "result.json"), Result{
		Run: r.ID, Scenario: r.Scenario, SHA: r.SHA, Dirty: r.Dirty, DiffSHA256: r.DiffSHA256,
		Source: r.Source, Result: r.Result, Steps: r.Steps, Checks: r.Checks, Verdicts: r.Verdicts,
	})
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// A unique temp file per write: the start goroutine and an RPC can save
	// the same run at once, and a shared temp name loses one rename.
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func readRun(path string) (Run, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: a run.json under serve's harness directory
	if err != nil {
		return Run{}, err
	}
	var r Run
	if err := json.Unmarshal(b, &r); err != nil {
		return Run{}, fmt.Errorf("read %s: %w", path, err)
	}
	return r, nil
}

// storedRuns reads every run.json of session, newest first.
func (m *Manager) storedRuns(session string) ([]Run, error) {
	if !safeIDRe.MatchString(session) {
		return nil, nil
	}
	entries, err := os.ReadDir(filepath.Join(m.opts.Dir, session))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var runs []Run
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		r, err := readRun(filepath.Join(m.opts.Dir, session, e.Name(), "run.json"))
		if err != nil {
			continue
		}
		runs = append(runs, r)
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].StartedAt.After(runs[j].StartedAt) })
	return runs, nil
}

// allStoredRuns reads every run.json on the host.
func (m *Manager) allStoredRuns() []Run {
	sessions, err := os.ReadDir(m.opts.Dir)
	if err != nil {
		return nil
	}
	var out []Run
	for _, s := range sessions {
		if s.IsDir() {
			runs, _ := m.storedRuns(s.Name())
			out = append(out, runs...)
		}
	}
	return out
}

// prune drops src/ and out/ of the session's stopped runs and keeps only the
// newest KeepRuns run directories.
func (m *Manager) prune(session string) {
	runs, err := m.storedRuns(session)
	if err != nil {
		return
	}
	for i, r := range runs {
		if r.Live() {
			continue
		}
		dir := m.runDir(session, r.ID)
		if i >= KeepRuns {
			_ = os.RemoveAll(dir)
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, "src"))
		_ = os.RemoveAll(filepath.Join(dir, "out"))
	}
}
