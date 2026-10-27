package jira

import (
	"encoding/json"
	"regexp"
	"strings"
)

// maxAcceptanceCriteria caps how many items one issue contributes to a session's
// Done-when block.
const maxAcceptanceCriteria = 20

var (
	acLabelRe    = regexp.MustCompile(`(?i)^(acceptance criteria|acceptance|definition of done|AC)\s*:?$`)
	mdHeadingRe  = regexp.MustCompile(`^#{1,6}\s+`)
	listMarkerRe = regexp.MustCompile(`^(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?`)
)

// ExtractAcceptanceCriteria returns the list items under the first
// "Acceptance criteria" / "Definition of done" heading or label in a markdown
// description, up to the next heading. A description without such a section
// yields nil, never the whole description.
func ExtractAcceptanceCriteria(markdown string) []string {
	lines := strings.Split(markdown, "\n")
	start := -1
	for i, line := range lines {
		if isAcceptanceLabel(line) {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil
	}
	var items []string
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		if mdHeadingRe.MatchString(trimmed) {
			break
		}
		if item, ok := listItem(trimmed); ok {
			items = append(items, item)
		}
	}
	return capItems(items)
}

// isAcceptanceLabel matches a heading line or a whole-line bold/plain label.
func isAcceptanceLabel(line string) bool {
	s := strings.TrimSpace(line)
	s = mdHeadingRe.ReplaceAllString(s, "")
	for _, wrap := range []string{"**", "__", "*", "_"} {
		if len(s) > 2*len(wrap) && strings.HasPrefix(s, wrap) && strings.HasSuffix(s, wrap) {
			s = strings.TrimSpace(s[len(wrap) : len(s)-len(wrap)])
			break
		}
	}
	return acLabelRe.MatchString(s)
}

// listItem strips a bullet, ordered or task-list marker. ok is false for a line
// that is not a list item or whose text is empty.
func listItem(line string) (string, bool) {
	loc := listMarkerRe.FindStringIndex(line)
	if loc == nil {
		return "", false
	}
	item := strings.TrimSpace(line[loc[1]:])
	return item, item != ""
}

// customFieldAcceptanceCriteria reads the configured acceptance-criteria field
// from the raw issue fields. The value may be ADF, a string, or an array of
// strings. List items win; a value without list items gives its non-empty lines.
func customFieldAcceptanceCriteria(rawFields json.RawMessage, field string) []string {
	if field == "" || len(rawFields) == 0 {
		return nil
	}
	var fields map[string]any
	if err := json.Unmarshal(rawFields, &fields); err != nil {
		return nil
	}
	var text string
	switch v := fields[field].(type) {
	case nil:
		return nil
	case []any:
		parts := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				parts = append(parts, s)
			}
		}
		text = strings.Join(parts, "\n")
	default:
		text = formatIssueDescription(v)
	}

	var items, plain []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		plain = append(plain, trimmed)
		if item, ok := listItem(trimmed); ok {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		items = plain
	}
	return capItems(items)
}

func capItems(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	if len(items) > maxAcceptanceCriteria {
		items = items[:maxAcceptanceCriteria]
	}
	return items
}
