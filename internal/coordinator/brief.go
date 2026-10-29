package coordinator

import (
	"fmt"
	"slices"
	"strings"
)

// Brief is what a coordinator hands a child: the design it is building to,
// the files it owns, the tests that define done, and what its report must
// contain.
type Brief struct {
	Objective, Contract, Owns, OutOfScope, AcceptanceTests, Report, EscalateWhen string
}

// briefWordFloor is the fewest words a brief's sections may hold in all.
const briefWordFloor = 8

// briefHeadings are the child brief's sections, in order.
var briefHeadings = []string{
	"Objective", "Contract", "Owns", "Out of scope", "Acceptance tests", "Report must contain", "Escalate when",
}

// Markers around the brief in a child's task file, so the brief can be read
// back apart from the JIRA text that follows it.
const (
	BriefStartMarker = "<!-- aiman:brief -->"
	BriefEndMarker   = "<!-- /aiman:brief -->"
)

// ChildGuidelines is appended to a child's brief, inside the markers.
const ChildGuidelines = "You are a child session. Your brief is your design: skip the Design first section of the aiman skill and do not wait for confirmation. " +
	"Do not edit the files listed under Acceptance tests. " +
	"Escalate instead of guessing when an Escalate when condition holds, or when the piece is harder than its brief rated it once you have explored the code: " +
	"run aiman todo add \"<question>\", then aiman session create --consult --for <todo-id> --brief-file <file>, where the file has exactly ## Question, ## Scope and ## Report must contain, " +
	"then wait for the [aiman] note with the verdict and the report path (the aiman skill's Escalating (children too) section has the details). " +
	"Write .aiman_report.md with ## Evidence (each command and its exit code) and ## Surprises (anything that affects other pieces, or \"none\")."

// ParseBrief reads a child brief with exactly the headings `## Objective`,
// `## Contract`, `## Owns`, `## Out of scope`, `## Acceptance tests`,
// `## Report must contain` and `## Escalate when`. Text before the first `##`
// heading is ignored. A missing, repeated, unknown or empty section, or fewer
// than 8 words in all, is an error of one line. It checks shape, not quality.
func ParseBrief(md string) (Brief, error) {
	t, err := parseSections(md, briefHeadings)
	if err != nil {
		return Brief{}, err
	}
	return Brief{Objective: t[0], Contract: t[1], Owns: t[2], OutOfScope: t[3],
		AcceptanceTests: t[4], Report: t[5], EscalateWhen: t[6]}, nil
}

// Markdown renders the brief with its seven headings, in order.
func (b Brief) Markdown() string {
	bodies := []string{b.Objective, b.Contract, b.Owns, b.OutOfScope, b.AcceptanceTests, b.Report, b.EscalateWhen}
	var sb strings.Builder
	for i, h := range briefHeadings {
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "## %s\n\n%s\n", h, bodies[i])
	}
	return sb.String()
}

// AcceptanceFiles are the repo-relative file paths named in AcceptanceTests.
func (b Brief) AcceptanceFiles() []string {
	var out []string
	for _, tok := range strings.Fields(b.AcceptanceTests) {
		tok = strings.Trim(tok, "`'\"()[]{}<>,.;:*")
		if isRepoPath(tok) && !slices.Contains(out, tok) {
			out = append(out, tok)
		}
	}
	return out
}

// AcceptanceCommands are the backtick spans in AcceptanceTests that are not
// repo paths.
func (b Brief) AcceptanceCommands() []string {
	var out []string
	parts := strings.Split(b.AcceptanceTests, "`")
	for i := 1; i < len(parts); i += 2 {
		span := strings.TrimSpace(parts[i])
		if span != "" && !isRepoPath(span) && !slices.Contains(out, span) {
			out = append(out, span)
		}
	}
	return out
}

// BriefFromTaskFile reads the brief a child's task file carries between the
// brief markers. ok is false when the markers are absent or the text between
// them is not a valid brief.
func BriefFromTaskFile(task string) (Brief, bool) {
	_, rest, ok := strings.Cut(task, BriefStartMarker)
	if !ok {
		return Brief{}, false
	}
	body, _, ok := strings.Cut(rest, BriefEndMarker)
	if !ok {
		return Brief{}, false
	}
	body = strings.TrimSpace(body)
	body = strings.TrimSpace(strings.TrimSuffix(body, ChildGuidelines))
	b, err := ParseBrief(body)
	return b, err == nil
}

// consultHeadings are a consult brief's sections, in order.
var consultHeadings = []string{"Question", "Scope", "Report must contain"}

// ConsultBrief is what a coordinator hands a consult: the design question,
// what the consult may read, and what its report must contain.
type ConsultBrief struct {
	Question, Scope, Report string
}

// ParseConsultBrief reads a consult's brief: exactly the headings
// `## Question`, `## Scope` and `## Report must contain`, under the same rules
// as a child brief of those three headings. Text before the first `##`
// heading is ignored; a missing, repeated, unknown or empty section, or fewer
// than 8 words in all, is an error of one line.
func ParseConsultBrief(md string) (ConsultBrief, error) {
	text, err := parseSections(md, consultHeadings)
	if err != nil {
		return ConsultBrief{}, err
	}
	return ConsultBrief{Question: text[0], Scope: text[1], Report: text[2]}, nil
}

// Markdown renders the consult brief with its three headings, in order.
func (b ConsultBrief) Markdown() string {
	return fmt.Sprintf("## %s\n\n%s\n\n## %s\n\n%s\n\n## %s\n\n%s\n",
		consultHeadings[0], b.Question, consultHeadings[1], b.Scope, consultHeadings[2], b.Report)
}

// parseSections splits md into the bodies of headings, in that order. Every
// heading must appear once with a non-empty body, no other `##` heading may
// appear, and the bodies must hold at least briefWordFloor words in all.
func parseSections(md string, headings []string) ([]string, error) {
	bodies := map[string]*strings.Builder{}
	var cur *strings.Builder
	for _, line := range strings.Split(md, "\n") {
		heading, ok := strings.CutPrefix(strings.TrimSpace(line), "## ")
		if !ok {
			if cur != nil {
				cur.WriteString(line + "\n")
			}
			continue
		}
		heading = strings.TrimSpace(heading)
		if !slices.Contains(headings, heading) {
			return nil, fmt.Errorf("brief: unexpected heading ## %s; use ## %s", heading, strings.Join(headings, ", ## "))
		}
		if bodies[heading] != nil {
			return nil, fmt.Errorf("brief: ## %s appears twice", heading)
		}
		cur = &strings.Builder{}
		bodies[heading] = cur
	}
	text := make([]string, len(headings))
	words := 0
	for i, h := range headings {
		body, ok := bodies[h]
		if !ok {
			return nil, fmt.Errorf("brief: missing ## %s", h)
		}
		text[i] = strings.TrimSpace(body.String())
		if text[i] == "" {
			return nil, fmt.Errorf("brief: ## %s is empty", h)
		}
		words += len(strings.Fields(text[i]))
	}
	if words < briefWordFloor {
		return nil, fmt.Errorf("brief: %d words; at least %d are needed", words, briefWordFloor)
	}
	return text, nil
}
