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

// Clean applies the full cleaning pipeline to raw tmux pane content:
//  1. Strip ANSI/VT100 escape sequences
//  2. Collapse consecutive duplicate lines into "line [×N]"
//  3. Collapse runs of blank lines into a single blank line
//  4. Drop known high-volume/low-signal noise (package manager chatter,
//     progress bars, timestamped application log lines, etc.) while
//     preserving everything else — user prompts, agent conversation,
//     commands, errors, build output, test results, etc.
//
// The blacklist approach (drop noise, keep everything else) ensures user
// prompts and agent responses are preserved for AI summarisation.
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

// scNoise matches lines that are high-volume command log output with little
// signal value. These are suppressed, keeping everything else.
var scNoise = []*regexp.Regexp{
	// npm / yarn / pnpm package manager chatter
	regexp.MustCompile(`(?i)^npm (warn|notice)\s+(deprecated|EBADENGINE|peer|old lockfile|fund)\b`),
	regexp.MustCompile(`(?i)^(yarn|pnpm) (warn|info)\s`),
	regexp.MustCompile(`(?i)^\d+ packages? (are looking|fund)`),
	regexp.MustCompile(`(?i)^Run \x60npm fund\x60`),

	// Go module download / verify progress
	regexp.MustCompile(`^go: (downloading|extracting|finding module providing|verifying)\s`),

	// Cargo / Rust compilation progress (not errors)
	regexp.MustCompile(`^\s+(Compiling|Downloaded|Downloading|Fetching|Checking|Updating|Locking)\s+\S`),

	// pip / uv / poetry install progress
	regexp.MustCompile(`^(Collecting |  Downloading |Downloading |Building |Preparing metadata|Installing collected packages|Requirement already satisfied|Obtaining )\S`),
	regexp.MustCompile(`^Successfully installed\s`),

	// Docker build layer noise
	regexp.MustCompile(`^(Step \d+/\d+ :|Sending build context|  ---> |Removing intermediate container)\s*`),
	regexp.MustCompile(`(?i)^(Pulling from|Pulling|Waiting|Verifying Checksum|Download complete|Pull complete|Extracting|Already exists|Digest:|Status: Downloaded)\s`),

	// Timestamped application log entries (high-volume in long sessions)
	regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}`),
	regexp.MustCompile(`^\[\d{4}-\d{2}-\d{2}`),
	regexp.MustCompile(`^[A-Z]{3,5}\s+\d{4}-\d{2}-\d{2}`),

	// Progress bars / spinners / percentage lines
	regexp.MustCompile(`[|\\/#=]{4,}|\.{5,}|^\s*\d+%\s`),

	// apt/apk/brew package manager progress
	regexp.MustCompile(`^(Get:|Hit:|Ign:|Reading|Fetched|Preparing to|Selecting|Unpacking|Setting up|Processing triggers)\s`),
	regexp.MustCompile(`^(==> |==> Downloading|==> Pouring|Already installed|Downloading https?://)\s*`),
}

// isNoise returns true if the line matches any known high-volume/low-signal
// command log pattern. Noise takes precedence over signal patterns.
func isNoise(l string) bool {
	for _, re := range scNoise {
		if re.MatchString(l) {
			return true
		}
	}
	return false
}

// structuralCompress is pass 4: drop only known noise lines, keep everything else.
// This preserves user prompts, agent conversation, and contextual content while
// still removing high-volume low-signal patterns (package manager chatter,
// progress bars, timestamped log entries, etc.).
func structuralCompress(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if isNoise(l) {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}
