package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// State is serve's record of one review session, kept in
// <dir>/<session id>.json (dir is ~/.aiman/pr-reviews on a host).
type State struct {
	// Ref is the PR under review, "owner/repo#n".
	Ref string `json:"ref"`
	// HeadSHA is the PR head the worktree was staged from.
	HeadSHA string `json:"head_sha"`
	// Revealed is set once the PR description has been written.
	Revealed bool `json:"revealed"`
	// Single records that the review went ahead with one reviewer.
	Single bool `json:"single"`
	// Fixes records each fixer round, oldest first.
	Fixes []FixRound `json:"fixes,omitempty"`
	// Assessment is the consult run for the current head.
	Assessment *AssessmentState `json:"assessment,omitempty"`
	// ApproveGate is what gate:approve was opened for.
	ApproveGate *AssessmentState `json:"approve_gate,omitempty"`
	// ApprovedHead is the head an approval was posted at.
	ApprovedHead string `json:"approved_head,omitempty"`
}

// FixRound is one fixer session's pass over the review's findings.
type FixRound struct {
	Fixer     string   `json:"fixer"`
	Findings  []string `json:"findings"`
	Fixed     []string `json:"fixed"`
	StartSHA  string   `json:"start_sha"`
	PushedSHA string   `json:"pushed_sha"`
}

// AssessmentState names a consult and the head it assessed.
type AssessmentState struct {
	Consult string `json:"consult"`
	HeadSHA string `json:"head_sha"`
}

// FixedMap maps a finding id to the sha that fixed it, for ids in a round's
// Fixed. A later round wins; a round that never pushed does not count.
func (s State) FixedMap() map[string]string {
	m := map[string]string{}
	for _, r := range s.Fixes {
		if r.PushedSHA == "" {
			continue
		}
		for _, id := range r.Fixed {
			m[id] = r.PushedSHA
		}
	}
	return m
}

// statePath is the state file of session id in dir. The id must be a plain
// file name.
func statePath(dir, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return "", fmt.Errorf("review state: session id %q is not a file name", id)
	}
	return filepath.Join(dir, id+".json"), nil
}

// LoadState reads the state of session id. A missing file is os.ErrNotExist.
func LoadState(dir, id string) (State, error) {
	path, err := statePath(dir, id)
	if err != nil {
		return State{}, err
	}
	raw, err := os.ReadFile(path) //nolint:gosec // path is dir plus a validated file name
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{}, fmt.Errorf("review state %s: %w", id, err)
	}
	return st, nil
}

// SaveState writes the state of session id atomically.
func SaveState(dir, id string, st State) error {
	path, err := statePath(dir, id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// FixReport is a fixer's .aiman_report.md.
type FixReport struct {
	Fixed, NotFixed map[string]string
	Surprises       string
}

var fixLineRe = regexp.MustCompile(`^(\S+)\s+(fixed|not fixed)\b\s*(.*)$`)

// ParseFixReport parses a fixer report: "## Findings" lines "F1 fixed
// <evidence>" or "F3 not fixed <reason>", and a required "## Surprises".
func ParseFixReport(md string) (FixReport, error) {
	r := FixReport{Fixed: map[string]string{}, NotFixed: map[string]string{}}
	var section string
	var surprises []string
	sawSurprises, sawFindings := false, false
	for _, line := range strings.Split(md, "\n") {
		line = strings.TrimSpace(line)
		if h, ok := strings.CutPrefix(line, "## "); ok {
			section = strings.ToLower(strings.TrimSpace(h))
			sawSurprises = sawSurprises || section == "surprises"
			continue
		}
		switch section {
		case "findings":
			if line == "" {
				continue
			}
			m := fixLineRe.FindStringSubmatch(line)
			if m == nil {
				return FixReport{}, fmt.Errorf("fix report: line %q: want \"F1 fixed <evidence>\" or \"F1 not fixed <reason>\"", line)
			}
			if strings.TrimSpace(m[3]) == "" {
				return FixReport{}, fmt.Errorf("fix report: %s needs evidence or a reason", m[1])
			}
			sawFindings = true
			if m[2] == "fixed" {
				r.Fixed[m[1]] = strings.TrimSpace(m[3])
			} else {
				r.NotFixed[m[1]] = strings.TrimSpace(m[3])
			}
		case "surprises":
			surprises = append(surprises, line)
		}
	}
	if !sawFindings {
		return FixReport{}, errors.New("fix report: ## Findings has no entries")
	}
	if !sawSurprises {
		return FixReport{}, errors.New("fix report: ## Surprises is required")
	}
	r.Surprises = strings.TrimSpace(strings.Join(surprises, "\n"))
	return r, nil
}
