package transcript

import (
	"regexp"
	"strconv"
	"strings"
)

// A notification's <summary> repeats the command it ran, which can hold a
// secret and can hold anything that looks like a tag. Only the fields before
// it are read, and the summary gives nothing but the digits of its final
// "(exit code N)".
var (
	claudeTaskIDRe     = regexp.MustCompile(`<task-id>([^<\n]*)</task-id>`)
	claudeOutputFileRe = regexp.MustCompile(`<output-file>([^<\n]*)</output-file>`)
	claudeStatusRe     = regexp.MustCompile(`<status>([^<\n]*)</status>`)
	claudeExitCodeRe   = regexp.MustCompile(`\(exit code (\d+)\)</summary>`)
	claudeSafeIDRe     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	claudeSafeStatusRe = regexp.MustCompile(`^[a-z_]{1,32}$`)
)

type claudeNotification struct {
	taskID     string
	outputFile string
	status     string
	exitCode   *int
}

func parseClaudeNotification(text string) claudeNotification {
	head, tail, _ := strings.Cut(text, "<summary>")
	n := claudeNotification{
		taskID:     firstClaudeGroup(claudeTaskIDRe, head),
		outputFile: firstClaudeGroup(claudeOutputFileRe, head),
		status:     firstClaudeGroup(claudeStatusRe, head),
	}
	if !claudeSafeIDRe.MatchString(n.taskID) {
		n.taskID = ""
	}
	if !claudeSafeStatusRe.MatchString(n.status) {
		n.status = ""
	}
	if all := claudeExitCodeRe.FindAllStringSubmatch(tail, -1); len(all) > 0 {
		if code, err := strconv.Atoi(all[len(all)-1][1]); err == nil {
			n.exitCode = &code
		}
	}
	return n
}

func firstClaudeGroup(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// notification adds the system item and moves whatever the task id names to
// its end state.
func (c *claude) notification(e *claudeEvent) {
	n := parseClaudeNotification(e.plainText())
	proc := c.processes[n.taskID]
	sub := c.subagents[n.taskID]
	c.addItem(e, Item{Kind: KindSystem, Text: claudeNotificationText(n, proc != nil, sub != nil)})
	if n.taskID != "" {
		if _, known := c.outputs[n.taskID]; !known {
			c.recordOutput(n.taskID, n.outputFile)
		}
	}
	if proc != nil {
		proc.Process.State = ProcessExited
		proc.Process.ExitCode = n.exitCode
		c.updateItem(e, proc)
	}
	if sub != nil {
		c.finishSubagent(e, sub, n)
	}
}

// claudeNotificationText is built from validated fields only.
func claudeNotificationText(n claudeNotification, isProcess, isSubagent bool) string {
	label := "Background task"
	switch {
	case isProcess:
		label = "Background command"
	case isSubagent:
		label = "Sub-agent"
	}
	parts := []string{label}
	if n.taskID != "" {
		parts = append(parts, n.taskID)
	}
	status := n.status
	if status == "" {
		status = "finished"
	}
	parts = append(parts, status)
	text := strings.Join(parts, " ")
	if n.exitCode != nil {
		text += " (exit code " + strconv.Itoa(*n.exitCode) + ")"
	}
	return text
}

// finishSubagent trusts the notification's status, and the meta file when the
// notification has none.
func (c *claude) finishSubagent(e *claudeEvent, it *Item, n claudeNotification) {
	s := it.Subagent
	meta, hasMeta := c.subagentMeta(s.ID)
	if hasMeta {
		applySubagentMeta(s, meta)
	}
	switch {
	case n.status == "completed":
		endSubagent(s, SubagentDone, e.at)
	case n.status != "":
		endSubagent(s, SubagentFailed, e.at)
	case hasMeta && meta.Finished:
		endSubagent(s, SubagentDone, e.at)
	}
	c.updateItem(e, it)
}
