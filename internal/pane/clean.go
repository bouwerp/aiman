// Package pane provides utilities for cleaning and compressing tmux pane output
// before it is sent to an AI model or persisted as a snapshot.
package pane

import (
	"fmt"
	"regexp"
	"strings"
)

// ansiEscape matches ANSI/VT100 escape sequences (colours, cursor moves, etc.).
var ansiEscape = regexp.MustCompile(`\x1b(\[[0-9;?]*[a-zA-Z]|\][^\x07]*\x07|[()][AB012]|[DABEGHM78=><]|%[Gg])`)

// Clean applies the full two-pass cleaning pipeline to raw tmux pane content:
//  1. Strip ANSI/VT100 escape sequences
//  2. Collapse consecutive duplicate lines into "line [×N]"
//  3. Collapse runs of blank lines into a single blank line
//  4. Structural compression: keep only signal-rich lines
//
// On typical agent terminal sessions this reduces character count by ~30%.
// The result is human/LLM readable and safe to pass directly to the model.
func Clean(s string) string {
	return structuralCompress(cleanLines(s))
}

// cleanLines performs passes 1–3 (ANSI strip + dedup + blank-collapse).
func cleanLines(s string) string {
	s = ansiEscape.ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	prevLine := ""
	dupCount := 0
	blankRun := 0

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t\r")
		if line == "" {
			blankRun++
			if blankRun == 1 {
				out = append(out, "")
			}
			if dupCount > 1 {
				out[len(out)-2] = fmt.Sprintf("%s [×%d]", prevLine, dupCount)
			}
			prevLine = ""
			dupCount = 0
			continue
		}
		blankRun = 0
		if line == prevLine {
			dupCount++
			continue
		}
		if dupCount > 1 && len(out) > 0 {
			out[len(out)-1] = fmt.Sprintf("%s [×%d]", prevLine, dupCount)
		}
		out = append(out, line)
		prevLine = line
		dupCount = 1
	}
	if dupCount > 1 && len(out) > 0 {
		out[len(out)-1] = fmt.Sprintf("%s [×%d]", prevLine, dupCount)
	}
	return strings.Join(out, "\n")
}

var (
	scCmdLine   = regexp.MustCompile(`^\$\s`)
	scError     = regexp.MustCompile(`(?i)(error|fail|panic|fatal|warn|except)`)
	scTestLine  = regexp.MustCompile(`^(ok\s|FAIL\s|---\s+(PASS|FAIL)|^PASS$|^FAIL$)`)
	scGitLine   = regexp.MustCompile(`^(\[[\w/. ]+\]|\s+\w.*\|\s+\d|\s+\d+ file)`)
	scBuildDiag = regexp.MustCompile(`^[\w./]+\.go:\d+:`)
)

// structuralCompress is pass 4: keep only signal-rich lines.
func structuralCompress(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l == "" {
			continue
		}
		if scCmdLine.MatchString(l) || scError.MatchString(l) || scTestLine.MatchString(l) ||
			scGitLine.MatchString(l) || scBuildDiag.MatchString(l) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
