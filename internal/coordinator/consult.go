package coordinator

import (
	"errors"
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// ErrConsultNeedsSmart means the smart tier has no installed agent. A consult
// never falls back to the worker tier: a lightweight reviewer adds nothing.
var ErrConsultNeedsSmart = errors.New("a consult needs an installed agents.smart entry")

// PickConsult chooses the consult's agent from the installed entries of the
// smart tier. It prefers, in order: an entry from another model family than
// the coordinator's; an entry independent of the coordinator under the
// reviewer rule (another binary, or the same binary on another named model);
// the first installed entry. The returned entry's Agent is its command.
func PickConsult(smart []config.PreferredAgent, coordAgent, coordModel string, installed func(string) bool) (config.PreferredAgent, error) {
	coordCmd := commandOf(coordAgent)
	coordFamily := agent.Family(coordCmd, coordModel)
	var independent, first *config.PreferredAgent
	for _, entry := range smart {
		command := entry.Command()
		if command == "" || !installed(command) {
			continue
		}
		entry.Agent = command
		family := agent.Family(command, entry.Model)
		if family != "" && coordFamily != "" && family != coordFamily {
			return entry, nil
		}
		if independent == nil && independentOf(entry, coordCmd, coordModel) {
			independent = &entry
		}
		if first == nil {
			first = &entry
		}
	}
	switch {
	case independent != nil:
		return *independent, nil
	case first != nil:
		return *first, nil
	}
	return config.PreferredAgent{}, ErrConsultNeedsSmart
}

// independentOf is verify.PickReviewer's rule: another binary, or the same
// binary on a named model other than the coordinator's.
func independentOf(entry config.PreferredAgent, coordCmd, coordModel string) bool {
	if commandOf(entry.Agent) != coordCmd {
		return true
	}
	model := strings.TrimSpace(entry.Model)
	return model != "" && model != strings.TrimSpace(coordModel)
}

// commandOf normalises an agent name or command to its binary, so a display
// name like "Claude Code" compares equal to "claude".
func commandOf(name string) string {
	if a, ok := agent.FindKnown(name); ok {
		return agent.CommandBase(a.Command)
	}
	return agent.CommandBase(name)
}

// Consult verdicts, the first word of a report's `## Verdict` section.
const (
	VerdictApprove = "approve"
	VerdictRevise  = "revise"
	VerdictReject  = "reject"
)

// ConsultReport is the shape of a consult's .aiman_report.md: how many
// objections of each severity it raised and its verdict.
type ConsultReport struct {
	High, Medium, Low int
	Verdict           string
}

// ParseConsultReport reads a consult's report. Bullets under `## Objections`
// that start with `[high]`, `[medium]` or `[low]` (any case) are counted; other
// lines are prose. `## Verdict` is required and its first word must be
// approve, revise or reject.
func ParseConsultReport(md string) (ConsultReport, error) {
	var r ConsultReport
	section, verdictSeen := "", false
	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)
		if heading, ok := strings.CutPrefix(trimmed, "## "); ok {
			section = strings.ToLower(strings.TrimSpace(heading))
			verdictSeen = verdictSeen || section == "verdict"
			continue
		}
		switch section {
		case "objections":
			r.count(trimmed)
		case "verdict":
			if r.Verdict == "" && trimmed != "" {
				r.Verdict = verdictWord(trimmed)
			}
		}
	}
	if !verdictSeen {
		return ConsultReport{}, errors.New("consult report: missing ## Verdict")
	}
	switch r.Verdict {
	case VerdictApprove, VerdictRevise, VerdictReject:
		return r, nil
	case "":
		return ConsultReport{}, errors.New("consult report: ## Verdict is empty")
	}
	return ConsultReport{}, fmt.Errorf("consult report: verdict %q is not approve, revise or reject", r.Verdict)
}

// count adds one objection when line is a bullet tagged with a severity.
func (r *ConsultReport) count(line string) {
	body, ok := strings.CutPrefix(line, "- ")
	if !ok {
		body, ok = strings.CutPrefix(line, "* ")
	}
	if !ok {
		return
	}
	tag := strings.ToLower(strings.TrimSpace(body))
	switch {
	case strings.HasPrefix(tag, "[high]"):
		r.High++
	case strings.HasPrefix(tag, "[medium]"):
		r.Medium++
	case strings.HasPrefix(tag, "[low]"):
		r.Low++
	}
}

// verdictWord is the first word of line, lowercased, without surrounding
// markup or punctuation.
func verdictWord(line string) string {
	word := strings.Fields(line)[0]
	return strings.ToLower(strings.Trim(word, "*_`:.,;!-"))
}
