package coordinator

import (
	"fmt"
	"slices"
	"strings"
)

// Brief is what a coordinator hands a child: the question to answer, the
// scope to stay in, and what the child's report must contain.
type Brief struct {
	Question, Scope, Report string
}

// briefWordFloor is the fewest words a brief's three sections may hold in all.
const briefWordFloor = 8

// briefHeadings are the brief's sections, in order.
var briefHeadings = []string{"Question", "Scope", "Report must contain"}

// ParseBrief reads a brief with exactly the headings `## Question`,
// `## Scope` and `## Report must contain`. Text before the first `##` heading
// is ignored. A missing, repeated, unknown or empty section, or fewer than 8
// words in all, is an error of one line. It checks shape, not quality.
func ParseBrief(md string) (Brief, error) {
	bodies := map[string]*strings.Builder{}
	var cur *strings.Builder
	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)
		if heading, ok := strings.CutPrefix(trimmed, "## "); ok {
			heading = strings.TrimSpace(heading)
			if !slices.Contains(briefHeadings, heading) {
				return Brief{}, fmt.Errorf("brief: unexpected heading ## %s; use ## Question, ## Scope, ## Report must contain", heading)
			}
			if bodies[heading] != nil {
				return Brief{}, fmt.Errorf("brief: ## %s appears twice", heading)
			}
			cur = &strings.Builder{}
			bodies[heading] = cur
			continue
		}
		if cur != nil {
			cur.WriteString(line + "\n")
		}
	}
	text := make([]string, len(briefHeadings))
	words := 0
	for i, h := range briefHeadings {
		body, ok := bodies[h]
		if !ok {
			return Brief{}, fmt.Errorf("brief: missing ## %s", h)
		}
		text[i] = strings.TrimSpace(body.String())
		if text[i] == "" {
			return Brief{}, fmt.Errorf("brief: ## %s is empty", h)
		}
		words += len(strings.Fields(text[i]))
	}
	if words < briefWordFloor {
		return Brief{}, fmt.Errorf("brief: %d words; at least %d are needed", words, briefWordFloor)
	}
	return Brief{Question: text[0], Scope: text[1], Report: text[2]}, nil
}

// Markdown renders the brief with its three headings, in order.
func (b Brief) Markdown() string {
	return fmt.Sprintf("## %s\n\n%s\n\n## %s\n\n%s\n\n## %s\n\n%s\n",
		briefHeadings[0], b.Question, briefHeadings[1], b.Scope, briefHeadings[2], b.Report)
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
