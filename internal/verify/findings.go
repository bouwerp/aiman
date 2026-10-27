package verify

import (
	"fmt"
	"regexp"
	"strings"
)

// CompleteMarker is the last line a reviewer writes when it finished.
const CompleteMarker = "<!-- aiman:review-complete -->"

// Severity is how strongly a finding asks for a change.
type Severity string

const (
	SeverityRequired   Severity = "REQUIRED"
	SeveritySuggestion Severity = "SUGGESTION"
)

// Finding is one review finding.
type Finding struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	Title    string   `json:"title"`
	Location string   `json:"location"`
	Body     string   `json:"body"`
}

var (
	findingHeader = regexp.MustCompile(`(?i)^##\s*\[(REQUIRED|SUGGESTION)\]\s*(.+)$`)
	sectionHeader = regexp.MustCompile(`^##\s+`)
	locationRE    = regexp.MustCompile(`[\w./-]+:\d+`)
)

// ParseReview extracts findings and reports whether the completion marker is
// present. Unknown section headers end the previous finding and are ignored.
func ParseReview(text string) (findings []Finding, complete bool) {
	complete = strings.Contains(text, CompleteMarker)
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); {
		match := findingHeader.FindStringSubmatch(strings.TrimSpace(lines[i]))
		if match == nil {
			i++
			continue
		}
		j := i + 1
		for j < len(lines) {
			line := strings.TrimSpace(lines[j])
			if sectionHeader.MatchString(lines[j]) || line == CompleteMarker {
				break
			}
			j++
		}
		body := strings.TrimSpace(strings.Join(lines[i+1:j], "\n"))
		findings = append(findings, Finding{
			ID:       fmt.Sprintf("f%d", len(findings)+1),
			Severity: Severity(strings.ToUpper(match[1])),
			Title:    strings.TrimSpace(match[2]),
			Location: locationRE.FindString(body),
			Body:     body,
		})
		i = j
	}
	return findings, complete
}
