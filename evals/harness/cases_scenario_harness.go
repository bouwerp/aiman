package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	scenario "github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/server"
)

// scenarioHarnessFile is the repository's .aiman/harness.yaml: one scenario
// with two tester steps and a check.
const scenarioHarnessFile = `version: 1
app:
  run: serve
  ports: [{name: web, container: 8080}]
scenarios:
  - id: refund
    steps:
      - {do: "Open {{web}}/orders/42.", expect: "Order 42 is paid"}
      - {do: "Press Refund.", expect: "Refunded"}
    checks:
      - {name: refunded, run: "test -f /tmp/refunded"}
`

// scenarioRepo commits the harness file in a fresh repository under the
// harness directory and registers s1 on it, working so steering notes stay
// queued where a case can read them.
func scenarioRepo(h *Harness) (string, error) {
	base, err := filepath.EvalSymlinks(h.Dir)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "app")
	if err := os.MkdirAll(filepath.Join(dir, ".aiman"), 0o750); err != nil {
		return "", err
	}
	files := map[string]string{
		scenario.FilePath: scenarioHarnessFile,
		"go.mod":          "module x\n\ngo 1.25\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			return "", err
		}
	}
	for _, args := range [][]string{{"init", "-q", "-b", "feat"}, {"add", "-A"}, {"commit", "-qm", "init"}} {
		if err := evalGit(dir, args...); err != nil {
			return "", err
		}
	}
	h.RegisterSession(domain.Session{
		ID: "s1", WorktreePath: dir, WorkingDirectory: dir, RepoName: "o/r", Branch: "feat", AgentName: "claude",
		HookState: domain.AgentStateWorking, HookStateAt: time.Now().UTC(),
	})
	return dir, nil
}

// decodeResult turns a response's result into out.
func decodeResult(result any, out any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

type scenarioRunResult struct {
	Type string       `json:"type"`
	Run  scenario.Run `json:"run"`
}

// scenarioUp asks for a run of refund as the operator and waits for it.
func scenarioUp(h *Harness, extra map[string]any) (scenario.Run, error) {
	params := map[string]any{"session": "s1", "scenario": "refund", "wait": true}
	for k, v := range extra {
		params[k] = v
	}
	resp, err := h.Call("harness.up", params)
	if err != nil {
		return scenario.Run{}, err
	}
	var res scenarioRunResult
	if err := decodeResult(resp.Result, &res); err != nil {
		return scenario.Run{}, err
	}
	if res.Type != "harness_run" {
		return res.Run, fmt.Errorf("harness.up answered type %q", res.Type)
	}
	return res.Run, nil
}

// ledgerKinds lists the harness kinds on s1's ledger, in order.
func ledgerKinds(h *Harness) []domain.EventKind {
	var out []domain.EventKind
	for _, e := range h.Events("s1") {
		if strings.HasPrefix(string(e.Kind), "harness.") {
			out = append(out, e.Kind)
		}
	}
	return out
}

// CaseScenarioHarness drives the scenario harness through serve with a fake
// Docker: list, up, check, an operator's failed step, a session's refused
// verdict and down. The failed step becomes the to-do
// `Harness: refund step 2: <note>` with Ref harness:refund:2 and a steering
// note, and the ledger holds every harness kind in order.
func CaseScenarioHarness() Case {
	return Case{Name: "scenario-harness", Assert: func(h *Harness) []MetricResult {
		docker := NewFakeDocker(FakeDockerImages...)
		if err := h.EnableScenarioHarness(docker); err != nil {
			return []MetricResult{score("fixture", false, err.Error())}
		}
		if _, err := scenarioRepo(h); err != nil {
			return []MetricResult{score("fixture", false, err.Error())}
		}

		var list struct {
			Type      string              `json:"type"`
			Source    string              `json:"source"`
			Scenarios []scenario.Scenario `json:"scenarios"`
		}
		resp, lerr := h.Call("harness.list", map[string]any{"session": "s1"})
		if lerr == nil {
			lerr = decodeResult(resp.Result, &list)
		}
		listed := lerr == nil && list.Type == "harness_list" && list.Source == string(scenario.SourceFile) &&
			len(list.Scenarios) == 1 && list.Scenarios[0].ID == "refund" && len(list.Scenarios[0].Steps) == 2

		run, uerr := scenarioUp(h, nil)
		if uerr != nil {
			return []MetricResult{score("harness_listed", listed, fmt.Sprintf("%+v %v", list, lerr)), score("harness_up", false, uerr.Error())}
		}
		ready := run.State == scenario.StateReady && run.Session == "s1" && !run.Dirty &&
			len(run.Ports) == 1 && run.Ports[0].HostPort == FakeDockerHostPort

		var checked struct {
			Type   string              `json:"type"`
			Checks []scenario.CheckRun `json:"checks"`
		}
		resp, cerr := h.Call("harness.check", map[string]any{"run": run.ID})
		if cerr == nil {
			cerr = decodeResult(resp.Result, &checked)
		}
		checkPassed := cerr == nil && len(checked.Checks) == 1 && checked.Checks[0].Exit != nil && *checked.Checks[0].Exit == 0
		checkInSandbox := slices.ContainsFunc(docker.ComposeArgs(), func(a string) bool {
			return strings.HasPrefix(a, "run --rm --no-deps "+scenario.ServiceApp+" sh -c test -f /tmp/refunded")
		})

		note := "UI says Refunded but orders.status stays paid\nsecond line"
		var verdict struct {
			Type string `json:"type"`
			Todo string `json:"todo"`
		}
		resp, verr := h.Call("harness.verdict", map[string]any{"run": run.ID, "step": 2, "result": "fail", "note": note})
		if verr == nil {
			verr = decodeResult(resp.Result, &verdict)
		}
		wantText := "Harness: refund step 2: UI says Refunded but orders.status stays paid"
		var item domain.TodoItem
		if verr == nil && verdict.Todo != "" {
			item, _ = h.TodoItem("s1", verdict.Todo)
		}
		todoOK := item.Text == wantText && item.Ref == "harness:refund:2" && item.State == domain.TodoPending
		steered := waitUntil(3*time.Second, func() bool {
			return slices.ContainsFunc(h.SteerPending("s1"), func(n server.SteerNote) bool {
				return strings.Contains(n.Text, "harness scenario refund step 2 fail") && strings.Contains(n.Text, verdict.Todo)
			})
		})

		_, sessionVerdict := h.CallAs("s1", "harness.verdict", map[string]any{"run": run.ID, "step": 1, "result": "pass"})

		resp, derr := h.Call("harness.down", map[string]any{"run": run.ID})
		var down scenarioRunResult
		if derr == nil {
			derr = decodeResult(resp.Result, &down)
		}
		stopped := derr == nil && down.Run.State == scenario.StateStopped

		kinds := ledgerKinds(h)
		want := []domain.EventKind{
			domain.EventHarnessStarted, domain.EventHarnessReady, domain.EventHarnessCheck,
			domain.EventHarnessVerdict, domain.EventHarnessStopped,
		}
		return []MetricResult{
			score("harness_listed", listed, fmt.Sprintf("%+v %v", list, lerr)),
			score("harness_up_ready", ready, fmt.Sprintf("%+v", run)),
			score("harness_check_runs", checkPassed && checkInSandbox, fmt.Sprintf("%+v %v %v", checked, cerr, docker.ComposeArgs())),
			score("harness_fail_adds_todo", todoOK, fmt.Sprintf("verdict %+v %v, item %+v", verdict, verr, item)),
			score("harness_fail_steers", steered, fmt.Sprintf("pending %+v", h.SteerPending("s1"))),
			score("harness_verdict_needs_operator", errorCode(sessionVerdict) == "needs_operator", errString(sessionVerdict)),
			score("harness_down_stops", stopped, fmt.Sprintf("%+v %v", down.Run, derr)),
			score("harness_ledger_complete", slices.Equal(kinds, want), fmt.Sprintf("ledger %v", kinds)),
		}
	}}
}

// CaseScenarioHarnessDirty refuses a run of an uncommitted tree with
// harness_dirty naming the path, then runs it with dirty:true and records the
// diff hash.
func CaseScenarioHarnessDirty() Case {
	return Case{Name: "scenario-harness-dirty", Assert: func(h *Harness) []MetricResult {
		if err := h.EnableScenarioHarness(NewFakeDocker(FakeDockerImages...)); err != nil {
			return []MetricResult{score("fixture", false, err.Error())}
		}
		dir, err := scenarioRepo(h)
		if err != nil {
			return []MetricResult{score("fixture", false, err.Error())}
		}
		if err := os.WriteFile(filepath.Join(dir, "wip.go"), []byte("package x\n"), 0o600); err != nil {
			return []MetricResult{score("fixture", false, err.Error())}
		}
		resp, refused := h.Call("harness.up", map[string]any{"session": "s1", "scenario": "refund"})
		var paths []any
		if resp.Error != nil {
			data, _ := resp.Error.Data.(map[string]any)
			paths, _ = data["paths"].([]any)
		}
		refusedDirty := errorCode(refused) == scenario.CodeDirty && slices.Contains(paths, any("wip.go"))
		startedNothing := len(ledgerKinds(h)) == 0

		run, uerr := scenarioUp(h, map[string]any{"dirty": true})
		dirtyRun := uerr == nil && run.State == scenario.StateReady && run.Dirty && len(run.DiffSHA256) == 64 && run.SHA != ""
		var recorded bool
		for _, e := range h.Events("s1") {
			if e.Kind != domain.EventHarnessStarted {
				continue
			}
			var p map[string]any
			recorded = json.Unmarshal(e.Payload, &p) == nil && p["dirty"] == true && p["diff_sha256"] == run.DiffSHA256
		}
		return []MetricResult{
			score("harness_dirty_refused", refusedDirty, fmt.Sprintf("%v data paths %v", errString(refused), paths)),
			score("harness_dirty_refusal_starts_nothing", startedNothing, fmt.Sprintf("ledger %v", ledgerKinds(h))),
			score("harness_dirty_run", dirtyRun, fmt.Sprintf("%+v %v", run, uerr)),
			score("harness_dirty_recorded", recorded, "harness.started lacks dirty and diff_sha256"),
		}
	}}
}
