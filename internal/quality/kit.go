package quality

import (
	"strings"
	"unicode"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	// DoneItemText is the pinned last to-do of a session with conditions.
	DoneItemText = "Done-when met"
	// DoneWhenFindingPrefix starts the title of a verifier finding for an
	// unmet condition.
	DoneWhenFindingPrefix = "Done-when not met: "
	// KitPrompt is the initial prompt of a repository session whose task
	// file holds only the kit.
	KitPrompt = "Read .aiman_task.md — it lists when this session is done and the checklists that apply to it."
)

const (
	doneWhenOpen    = "<!-- aiman:done-when -->"
	doneWhenClose   = "<!-- /aiman:done-when -->"
	checklistsOpen  = "<!-- aiman:checklists -->"
	checklistsClose = "<!-- /aiman:checklists -->"

	doneWhenFooter  = "The last to-do, `" + DoneItemText + "`, is yours to tick with `aiman todo update <id> --state done` only when every condition above holds. A reviewer checks each one."
	checklistsIntro = "Before you finish, work through each checklist that applies to the files you change:"
)

// DoneWhen lists the acceptance criteria first, then the repository
// defaults, dropping blanks and entries equal after normalisation.
func DoneWhen(acceptance, repoDefault []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, list := range [][]string{acceptance, repoDefault} {
		for _, c := range list {
			c = strings.TrimSpace(c)
			key := normalize(c)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, c)
		}
	}
	return out
}

// normalize matches todo.Normalize: punctuation dropped, lower case,
// whitespace collapsed. It is copied so this package stays import-free of
// internal/todo.
func normalize(text string) string {
	stripped := strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, text)
	return strings.Join(strings.Fields(stripped), " ")
}

// RenderDoneWhenBlock renders the Done-when block, or "" for no conditions.
func RenderDoneWhenBlock(conds []string) string {
	if len(conds) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(doneWhenOpen + "\n## Done when\n\n")
	for _, c := range conds {
		sb.WriteString("- [ ] " + c + "\n")
	}
	sb.WriteString("\n" + doneWhenFooter + "\n" + doneWhenClose + "\n")
	return sb.String()
}

// RenderChecklistBlock renders the checklist block, or "" for no checklists.
func RenderChecklistBlock(lists []domain.Checklist) string {
	if len(lists) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(checklistsOpen + "\n## Checklists\n\n" + checklistsIntro + "\n\n")
	for _, c := range lists {
		sb.WriteString("- " + c.Path + "\n")
	}
	sb.WriteString(checklistsClose + "\n")
	return sb.String()
}

// ReplaceKitBlocks replaces the kit blocks of a task file in place. A nil
// doneWhen leaves the existing Done-when block byte for byte; an empty
// block removes it. A block the task lacks is inserted next to the other
// block, else after the first "# Task" section, else at the end, in the
// order Done-when, Checklists.
func ReplaceKitBlocks(task string, doneWhen *string, checklists string) string {
	dwWanted := ""
	if doneWhen != nil {
		dwWanted = *doneWhen
		task = replaceBlock(task, doneWhenOpen, doneWhenClose, dwWanted)
	}
	task = replaceBlock(task, checklistsOpen, checklistsClose, checklists)

	_, dwEnd, dwFound := findBlock(task, doneWhenOpen, doneWhenClose)
	clStart, _, clFound := findBlock(task, checklistsOpen, checklistsClose)
	insertDW := dwWanted != "" && !dwFound
	insertCL := checklists != "" && !clFound

	switch {
	case insertDW && clFound:
		return task[:clStart] + dwWanted + "\n" + task[clStart:]
	case insertCL && dwFound:
		return task[:dwEnd] + "\n" + checklists + task[dwEnd:]
	}
	var blocks []string
	if insertDW {
		blocks = append(blocks, dwWanted)
	}
	if insertCL {
		blocks = append(blocks, checklists)
	}
	if len(blocks) == 0 {
		return task
	}
	text := strings.Join(blocks, "\n")
	if pos, ok := afterTaskSection(task); ok {
		before := task[:pos]
		if !strings.HasSuffix(before, "\n\n") {
			before += "\n"
		}
		return before + text + "\n" + task[pos:]
	}
	if task != "" && !strings.HasSuffix(task, "\n\n") {
		if !strings.HasSuffix(task, "\n") {
			task += "\n"
		}
		task += "\n"
	}
	return task + text
}

// findBlock returns the span of a block including the newline after its
// closing marker.
func findBlock(s, open, close string) (start, end int, ok bool) {
	start = strings.Index(s, open)
	if start < 0 {
		return 0, 0, false
	}
	rel := strings.Index(s[start:], close)
	if rel < 0 {
		return 0, 0, false
	}
	end = start + rel + len(close)
	if end < len(s) && s[end] == '\n' {
		end++
	}
	return start, end, true
}

func replaceBlock(s, open, close, block string) string {
	start, end, ok := findBlock(s, open, close)
	if !ok {
		return s
	}
	if block == "" && end < len(s) && s[end] == '\n' {
		end++
	}
	return s[:start] + block + s[end:]
}

// afterTaskSection returns where the first "# Task" section ends: the
// start of the "---" rule ahead of the next top-level heading, else the
// start of that heading.
func afterTaskSection(s string) (int, bool) {
	i := -1
	if strings.HasPrefix(s, "# Task\n") {
		i = 0
	} else if j := strings.Index(s, "\n# Task\n"); j >= 0 {
		i = j + 1
	}
	if i < 0 {
		return 0, false
	}
	from := i + len("# Task\n")
	if j := strings.Index(s[from:], "\n---\n\n# "); j >= 0 {
		return from + j + 1, true
	}
	if j := strings.Index(s[from:], "\n# "); j >= 0 {
		return from + j + 1, true
	}
	return 0, false
}

// ParseTaskKit reads the conditions and checklist paths back from a task
// file. A task without kit blocks yields nil, nil.
func ParseTaskKit(task string) (doneWhen []string, checklistPaths []string) {
	if start, end, ok := findBlock(task, doneWhenOpen, doneWhenClose); ok {
		for _, line := range strings.Split(task[start:end], "\n") {
			line = strings.TrimRight(line, "\r")
			for _, box := range []string{"- [ ] ", "- [x] ", "- [X] "} {
				if c, found := strings.CutPrefix(line, box); found {
					doneWhen = append(doneWhen, c)
					break
				}
			}
		}
	}
	if start, end, ok := findBlock(task, checklistsOpen, checklistsClose); ok {
		for _, line := range strings.Split(task[start:end], "\n") {
			if p, found := strings.CutPrefix(strings.TrimRight(line, "\r"), "- "); found && p != "" {
				checklistPaths = append(checklistPaths, p)
			}
		}
	}
	return doneWhen, checklistPaths
}

// KitOnlyTaskFile is the task file of a repository session without a JIRA
// issue: the scaffolding header and the kit blocks.
func KitOnlyTaskFile(doneWhen, checklists string) string {
	var sb strings.Builder
	sb.WriteString("<!--\n")
	sb.WriteString("DO NOT COMMIT — session scaffolding generated by Aiman.\n")
	sb.WriteString("-->\n\n")
	sb.WriteString("> **Do not commit this file.** It is generated for this session only and is listed in `.gitignore`.\n\n")
	sb.WriteString("# Session kit\n")
	return ReplaceKitBlocks(sb.String(), &doneWhen, checklists)
}
