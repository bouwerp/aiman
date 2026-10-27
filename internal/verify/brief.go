package verify

import (
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/quality"
	"github.com/bouwerp/aiman/internal/todo"
)

// InitialPrompt is the one-line prompt that starts a reviewer session.
const InitialPrompt = "Read .aiman_task.md and carry out the code review it describes. Write your findings to .aiman_review.md exactly in the format it gives, ending with the completion marker. Do not edit any other file, commit, push or open a pull request."

// BriefInput is what the reviewer needs to know about the worker.
type BriefInput struct {
	WorkerName string
	BaseRef    string
	Commit     string
	Diff       Diff
	Task       string
	Todos      []domain.TodoItem
	// DoneWhen are the worker's Done-when conditions, from its task file.
	DoneWhen []string
	// Rubric are the checklists the worker was asked to work through.
	Rubric []RubricItem
}

// RubricItem is one checklist the reviewer grades against. An empty Body
// means the checklist could not be read on the session host.
type RubricItem struct {
	Path string
	Body string
}

// doneWhenInstruction tells the reviewer how to report an unmet condition;
// the title prefix is what reopens the worker's Done-when item.
const doneWhenInstruction = "For every condition that does not hold in this snapshot, write a finding `## [REQUIRED] " +
	quality.DoneWhenFindingPrefix + "<condition>` explaining what is missing."

// BuildBrief renders the reviewer's .aiman_task.md.
func BuildBrief(in BriefInput) string {
	var b strings.Builder
	b.WriteString(domain.AimanScaffoldHeader)
	b.WriteString("# Code review requested\n\n")
	fmt.Fprintf(&b, "Review the snapshot of **%s** against `%s`.\n\n", in.WorkerName, in.BaseRef)
	fmt.Fprintf(&b, "The snapshot commit is `%s`.\n\n", in.Commit)

	b.WriteString("## Worker task\n\n")
	if strings.TrimSpace(in.Task) == "" {
		b.WriteString("No task file.\n\n")
	} else {
		b.WriteString(strings.TrimSpace(in.Task))
		b.WriteString("\n\n")
	}

	b.WriteString("## Worker to-do list\n\n")
	if list := todo.FormatList(domain.TodoList{Items: in.Todos}); list != "" {
		b.WriteString(list)
	} else {
		b.WriteString("No to-dos.")
	}
	b.WriteString("\n\n")
	writeRubric(&b, in.Rubric)
	writeDoneWhen(&b, in.DoneWhen)

	b.WriteString("## Diff\n\n")
	if in.Diff.Stat != "" {
		b.WriteString("```\n")
		b.WriteString(in.Diff.Stat)
		b.WriteString("\n```\n\n")
	}
	if in.Diff.Truncated {
		fmt.Fprintf(&b, "The diff below is truncated. Run `git diff %s %s` in the snapshot to see all of it.\n\n", in.Diff.MergeBase, in.Commit)
	}
	if in.Diff.Patch != "" {
		b.WriteString("```diff\n")
		b.WriteString(in.Diff.Patch)
		if !strings.HasSuffix(in.Diff.Patch, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("```\n\n")
	}

	b.WriteString("## Output format\n\n")
	b.WriteString("Write one section per finding:\n\n")
	b.WriteString("```\n## [REQUIRED] <short title>\n<path>:<line>\n<what is wrong and what to change>\n\n## [SUGGESTION] <short title>\n<path>:<line>\n<optional improvement>\n```\n\n")
	b.WriteString("Use REQUIRED only for a correctness, security or contract problem that must be fixed. End the file with this exact last line:\n\n")
	b.WriteString(CompleteMarker)
	b.WriteString("\n")
	return b.String()
}

func writeRubric(b *strings.Builder, rubric []RubricItem) {
	if len(rubric) == 0 {
		return
	}
	b.WriteString("## Review rubric\n\n")
	for _, r := range rubric {
		fmt.Fprintf(b, "### %s\n\n", r.Path)
		if body := strings.TrimSpace(r.Body); body != "" {
			b.WriteString(body)
		} else {
			b.WriteString("not readable")
		}
		b.WriteString("\n\n")
	}
}

func writeDoneWhen(b *strings.Builder, conds []string) {
	if len(conds) == 0 {
		return
	}
	b.WriteString("## Done-when conditions\n\n")
	for _, c := range conds {
		b.WriteString("- " + c + "\n")
	}
	b.WriteString("\n" + doneWhenInstruction + "\n\n")
}
