//go:build evalreal

package harness

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
)

// RealCase is one opt-in scenario run by an external agent driver. The driver
// gets AIMAN_EVAL_CASE and AIMAN_EVAL_OUT and must leave its evidence in that
// directory. The runner never launches a model itself, so a missing or
// misconfigured driver cannot silently pass.
type RealCase struct {
	Name   string
	Prompt string
}

var RealCases = []RealCase{
	{Name: "create", Prompt: "Create a small change in this worktree."},
	{Name: "prompt-once", Prompt: "Run one command and report its output once."},
	{Name: "todo-mirror", Prompt: "Make a two-item plan, then update one item."},
	{Name: "handover-restart", Prompt: "Write a short handover of what you changed."},
	{Name: "verify-pass", Prompt: "Make a tiny change that passes the test command."},
}

// RunRealAgent is compiled only with -tags evalreal. It is intentionally not
// part of make ci; set AIMAN_EVAL_REAL=1 and AIMAN_EVAL_DRIVER to a command
// that runs one case in the current worktree.
func RunRealAgent(t *testing.T) {
	if os.Getenv("AIMAN_EVAL_REAL") != "1" {
		t.Skip("set AIMAN_EVAL_REAL=1 to run the real-agent track")
	}
	driver := os.Getenv("AIMAN_EVAL_DRIVER")
	if driver == "" {
		t.Fatal("AIMAN_EVAL_DRIVER must name the real-agent driver")
	}
	budget := realBudget()
	runID := time.Now().UTC().Format("20060102-150405") + "-" + uuid.NewString()[:8]
	root := filepath.Join("..", "out", runID)
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	summary := realSummary{RunID: runID, Agent: os.Getenv("AIMAN_EVAL_AGENT"), BudgetUSD: budget, Cases: []realCaseResult{}}
	spent := 0.0
	for _, c := range RealCases {
		dir := filepath.Join(root, c.Name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		work := filepath.Join(root, "work", c.Name)
		if err := os.MkdirAll(work, 0o750); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		cmd := exec.CommandContext(ctx, driver) // #nosec G204 -- operator-supplied eval driver
		cmd.Dir = work
		cmd.Env = append(os.Environ(),
			"AIMAN_EVAL_CASE="+c.Name,
			"AIMAN_EVAL_PROMPT="+c.Prompt,
			"AIMAN_EVAL_OUT="+dir,
			"AIMAN_EVAL_AGENT="+os.Getenv("AIMAN_EVAL_AGENT"),
		)
		out, err := cmd.CombinedOutput()
		cancel()
		if werr := os.WriteFile(filepath.Join(dir, "driver.log"), out, 0o600); werr != nil {
			t.Fatal(werr)
		}
		cost := readRealCost(dir)
		spent += cost
		res := realCaseResult{Name: c.Name, CostUSD: cost, Passed: err == nil}
		if err != nil {
			res.Reason = err.Error()
		}
		summary.Cases = append(summary.Cases, res)
		if spent >= budget {
			summary.Capped = true
			summary.Cases = append(summary.Cases, realCaseResult{Name: "capped", Reason: "budget reached; remaining cases skipped"})
			break
		}
	}
	summary.SpentUSD = spent
	body, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "summary.json"), append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if summary.Capped {
		t.Logf("real-agent run %s capped at $%.2f; no judge", runID, spent)
		return
	}
	for _, c := range summary.Cases {
		if !c.Passed {
			t.Errorf("case %s: %s", c.Name, c.Reason)
		}
	}
}

type realSummary struct {
	RunID     string           `json:"run_id"`
	Agent     string           `json:"agent"`
	BudgetUSD float64          `json:"budget_usd"`
	SpentUSD  float64          `json:"spent_usd"`
	Capped    bool             `json:"capped"`
	Cases     []realCaseResult `json:"cases"`
}

type realCaseResult struct {
	Name    string  `json:"name"`
	Passed  bool    `json:"passed"`
	CostUSD float64 `json:"cost_usd"`
	Reason  string  `json:"reason,omitempty"`
}

func realBudget() float64 {
	raw := os.Getenv("AIMAN_EVAL_BUDGET_USD")
	if raw == "" {
		return 5
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 {
		return 5
	}
	return v
}

func readRealCost(dir string) float64 {
	raw, err := os.ReadFile(filepath.Join(dir, "cost.json"))
	if err != nil {
		return 0
	}
	var body struct {
		USD float64 `json:"usd"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return 0
	}
	return body.USD
}
