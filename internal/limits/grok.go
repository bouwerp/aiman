package limits

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bouwerp/aiman/internal/pane"
)

const (
	grokProvider = "grok"
	grokMinAge   = 10 * time.Minute
	grokTimeout  = 30 * time.Second

	grokTrustQuestion = "Do you trust the contents of this directory?"
	grokTrustYes      = "Yes, proceed"
	grokTrustNo       = "No, quit"
	grokUsageCommand  = "/usage"

	maxReasonRunes = 200
)

// GrokScreen is what a rendered Grok screen is showing.
type GrokScreen int

const (
	// GrokOther is any screen no key may be sent to.
	GrokOther GrokScreen = iota
	// GrokTrust is the trust prompt: Enter, once.
	GrokTrust
	// GrokComposer is an empty composer with no dialog: /usage, once.
	GrokComposer
	// GrokUsage is the usage panel: read it.
	GrokUsage
)

// ProbeConfig describes one Grok probe.
type ProbeConfig struct {
	// Command is the CLI and its arguments.
	Command []string
	// Dir is the fixed folder the CLI runs in.
	Dir string
	// Home is the HOME the CLI gets, and where its session folder is cleaned.
	Home string
	// PIDFile records the running probe.
	PIDFile string
	// Timeout is the hard limit for the whole run.
	Timeout time.Duration
}

var (
	grokComposerRow = regexp.MustCompile(`^\s*│\s*❯(.*)│\s*$`)
	grokPlan        = regexp.MustCompile(`Weekly limit \(([^)\n]+)\)`)
	grokPercent     = regexp.MustCompile(`[█░▓▒]+\s+(\d{1,3})%`)
	grokResets      = regexp.MustCompile(`(?m)Resets:[ \t]*([^│\n]*)`)
	// grokDialogWords marks a screen that may be asking something. A false match
	// costs one unknown reading; a miss could type /usage into a dialog, so the
	// list is broad. Footer words on the plain composer (always-approve, Quit,
	// Changelog) stay out of it.
	grokDialogWords = regexp.MustCompile(`(?i)\b(update|upgrade|install|download|restart|skip|proceed|trust|confirm|continue|accept|agree|allow|deny|login|log in|sign in|authenticate|press|enter|esc|y/n|error|failed|expired|limit reached)\b`)
)

// ClassifyGrokScreen classifies a rendered screen, with or without SGR codes.
func ClassifyGrokScreen(screen string) GrokScreen {
	text := pane.StripANSI(screen)
	rows := strings.Split(text, "\n")
	switch {
	case strings.Contains(text, "Weekly limit ("):
		return GrokUsage
	case isGrokTrust(text, rows):
		return GrokTrust
	case !grokDialogShowing(rows):
		if content, ok := grokComposerContent(rows); ok && content == "" {
			return GrokComposer
		}
	}
	return GrokOther
}

func isGrokTrust(text string, rows []string) bool {
	for _, phrase := range []string{grokTrustQuestion, grokTrustYes, grokTrustNo} {
		if !strings.Contains(text, phrase) {
			return false
		}
	}
	rest := make([]string, 0, len(rows))
	for _, row := range rows {
		if !strings.Contains(row, grokTrustQuestion) && !strings.Contains(row, grokTrustYes) {
			rest = append(rest, row)
		}
	}
	return !grokDialogShowing(rest)
}

// grokDialogShowing reports dialog text outside the folder path row and the
// rotating tip, whose wording says nothing about the screen.
func grokDialogShowing(rows []string) bool {
	for _, row := range rows {
		trimmed := strings.TrimSpace(row)
		if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "Tip:") {
			continue
		}
		if grokDialogWords.MatchString(trimmed) {
			return true
		}
	}
	return false
}

// grokComposerContent returns what the composer box holds. The box is the row
// with the prompt, directly above the bottom border.
func grokComposerContent(rows []string) (string, bool) {
	for i := len(rows) - 2; i >= 0; i-- {
		m := grokComposerRow.FindStringSubmatch(rows[i])
		if m != nil && strings.HasPrefix(strings.TrimSpace(rows[i+1]), "╰") {
			return strings.TrimSpace(m[1]), true
		}
	}
	return "", false
}

// grokReadyForEnter gates the Enter after /usage: the composer holds that text
// (or nothing, when the CLI does not echo until submit) and no dialog came up.
func grokReadyForEnter(screen string) bool {
	rows := strings.Split(pane.StripANSI(screen), "\n")
	content, ok := grokComposerContent(rows)
	return ok && (content == "" || content == grokUsageCommand) && !grokDialogShowing(rows)
}

// ParseGrokUsage reads the usage panel. It returns false unless the plan label,
// the percentage and the reset line are all present.
func ParseGrokUsage(screen string) (Reading, bool) {
	text := pane.StripANSI(screen)
	plan := grokPlan.FindStringSubmatch(text)
	percent := grokPercent.FindStringSubmatch(text)
	resets := grokResets.FindStringSubmatch(text)
	if plan == nil || percent == nil || resets == nil {
		return Reading{}, false
	}
	used, err := strconv.Atoi(percent[1])
	resetsText := strings.TrimSpace(resets[1])
	if err != nil || used > 100 || resetsText == "" {
		return Reading{}, false
	}
	return Reading{
		Provider: grokProvider,
		Plan:     strings.TrimSpace(plan[1]),
		Windows:  []Window{{Label: "week", UsedPercent: float64(used), ResetsText: resetsText}},
		State:    StateOK,
		Source:   "probe",
	}, true
}

// grokUnknown is a probe that ran and could not read the answer. Callers pass
// fixed text, never anything taken from the screen.
func grokUnknown(reason string) Reading {
	return Reading{Provider: grokProvider, State: StateUnknown, Reason: reason, Source: "probe"}
}

// sanitiseReason keeps a reason to one printable line with no home path.
func sanitiseReason(reason, home string) string {
	if reason == "" {
		return ""
	}
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, pane.StripANSI(reason))
	if home != "" {
		clean = strings.ReplaceAll(clean, home, "~")
	}
	clean = strings.Join(strings.Fields(clean), " ")
	if runes := []rune(clean); len(runes) > maxReasonRunes {
		clean = string(runes[:maxReasonRunes])
	}
	return clean
}

// grokPATH is the given PATH with the prefixes agents install under home.
func grokPATH(path, home string) string {
	return strings.Trim(strings.Join([]string{
		filepath.Join(home, ".npm-global", "bin"),
		path,
		filepath.Join(home, ".local", "bin"),
		"/usr/local/bin",
	}, ":"), ":")
}

// findExecutable searches path itself, because exec.LookPath reads the
// process's PATH and that lacks the agent prefixes.
func findExecutable(name, path string) string {
	for _, dir := range filepath.SplitList(path) {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	return ""
}

type grokSource struct{ env Env }

// NewGrokSource returns the probe source.
func NewGrokSource(env Env) Source { return grokSource{env: env} }

func (grokSource) Name() string           { return grokProvider }
func (grokSource) MinAge() time.Duration  { return grokMinAge }
func (grokSource) Timeout() time.Duration { return grokTimeout }

// Read reaps a probe a dead serve left behind, then runs a new one.
func (s grokSource) Read(ctx context.Context) Reading {
	cli := findExecutable(grokProvider, grokPATH(s.env.Getenv("PATH"), s.env.Home))
	if cli == "" {
		return Reading{Provider: grokProvider, State: StateUnavailable, Source: "probe"}
	}
	limitsDir := filepath.Join(s.env.Home, ".aiman", "limits")
	cfg := ProbeConfig{
		Command: []string{cli},
		Dir:     filepath.Join(limitsDir, "grok-probe"),
		Home:    s.env.Home,
		PIDFile: filepath.Join(limitsDir, "grok-probe.pid"),
		Timeout: grokTimeout,
	}
	ReapStaleProbe(cfg.PIDFile, cfg.Dir)
	r := RunGrokProbe(ctx, cfg)
	r.Reason = sanitiseReason(r.Reason, s.env.Home)
	if r.State == StateOK {
		r.ObservedAt = s.env.Now()
	}
	return r
}
