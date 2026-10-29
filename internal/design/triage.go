package design

import "strings"

// Key names a design page: the issue key, else the branch with every "/"
// replaced by "-".
func Key(issueKey, branch string) string {
	if k := strings.TrimSpace(issueKey); k != "" {
		return k
	}
	return strings.ReplaceAll(strings.TrimSpace(branch), "/", "-")
}

// CountTriage counts the rows of the table under "## Decisions" by their
// Triage column. The table header is "| Objection | Severity | Triage |
// Reason |"; other tables and other sections are ignored.
func CountTriage(md string) (accepted, rejected, deferred int) {
	inDecisions := false
	col := -1
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "## ") {
			inDecisions = strings.EqualFold(strings.TrimSpace(t[3:]), "Decisions")
			col = -1
			continue
		}
		if !inDecisions || !strings.HasPrefix(t, "|") {
			continue
		}
		cells := splitRow(t)
		if col < 0 {
			col = triageColumn(cells)
			continue
		}
		if col >= len(cells) {
			continue
		}
		switch strings.ToLower(cells[col]) {
		case "accept":
			accepted++
		case "reject":
			rejected++
		case "defer":
			deferred++
		}
	}
	return accepted, rejected, deferred
}

func splitRow(line string) []string {
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	cells := strings.Split(line, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

// triageColumn is the index of the Triage header cell, or -1 when the row is
// not a header naming it.
func triageColumn(cells []string) int {
	for i, c := range cells {
		if strings.EqualFold(c, "Triage") {
			return i
		}
	}
	return -1
}
