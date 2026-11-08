package ui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/remotesvc"
)

// githubReleaseEvery is how often the TUI asks GitHub for the latest release.
const githubReleaseEvery = 6 * time.Hour

// ReleaseHint names a release newer than the running TUI and where it was seen.
type ReleaseHint struct {
	Tag    string
	Source string // "remote" or "github"
}

// releaseHint returns the newest of the remote serve versions and the GitHub
// tag that is strictly newer than self. A self with no release number (a dev
// build) gets no hint, and unparsable inputs are ignored. A remote wins a tie
// with GitHub: the host already runs it.
func releaseHint(self string, remotes []string, github string) (ReleaseHint, bool) {
	cur, ok := remotesvc.ParseVersion(self)
	if !ok {
		return ReleaseHint{}, false
	}
	best, hint, found := cur, ReleaseHint{}, false
	consider := func(raw, source string) {
		v, ok := remotesvc.ParseVersion(raw)
		if !ok || !best.Less(v) {
			return
		}
		best, hint, found = v, ReleaseHint{Tag: v.String(), Source: source}, true
	}
	for _, r := range remotes {
		consider(r, "remote")
	}
	consider(github, "github")
	return hint, found
}

// Line is the help line's version slot while a newer release exists.
func (h ReleaseHint) Line(self string) string {
	return self + " · " + h.Tag + " available: aiman update"
}

// githubReleaseMsg carries the latest GitHub release tag; empty on any error.
type githubReleaseMsg struct{ tag string }

// githubReleaseDueMsg asks for the next GitHub check.
type githubReleaseDueMsg struct{}

// checkGitHubRelease asks GitHub for the latest release. Errors are dropped:
// the hint is a convenience and the remotes' versions still feed it.
func checkGitHubRelease() tea.Cmd {
	return func() tea.Msg {
		tag, err := remotesvc.LatestReleaseTag(context.Background())
		if err != nil {
			return githubReleaseMsg{}
		}
		return githubReleaseMsg{tag: tag}
	}
}

// applyGitHubRelease records the tag, refreshes the hint and schedules the
// next check. It is routed globally so the timer chain survives any screen.
func (m *Model) applyGitHubRelease(msg githubReleaseMsg) tea.Cmd {
	if msg.tag != "" {
		m.githubLatest = msg.tag
	}
	next := tea.Tick(githubReleaseEvery, func(time.Time) tea.Msg { return githubReleaseDueMsg{} })
	return tea.Batch(m.refreshReleaseHint(), next)
}

// refreshReleaseHint recomputes the hint from the probed serve versions and
// the GitHub tag, toasting a tag the first time it is seen in this run.
func (m *Model) refreshReleaseHint() tea.Cmd {
	var remotes []string
	for _, d := range m.daemons {
		if d.Kind == string(remotesvc.KindServe) {
			remotes = append(remotes, d.Version)
		}
	}
	hint, ok := releaseHint(m.version, remotes, m.githubLatest)
	if !ok {
		m.hint = nil
		return nil
	}
	m.hint = &hint
	if m.hintToasted == nil {
		m.hintToasted = map[string]bool{}
	}
	if m.hintToasted[hint.Tag] {
		return nil
	}
	m.hintToasted[hint.Tag] = true
	return m.showToast(hint.Tag+" available: aiman update", false, 8*time.Second)
}

// versionSlot is what the help line shows at its right edge.
func (m *Model) versionSlot() string {
	if m.hint == nil {
		return m.version
	}
	return m.hint.Line(m.version)
}

// hostSelfUpdating reports a host install started within OpTimeout. The probe
// reads the on-disk version, which stays old until the install swaps the
// binary, so the laptop must not start a second install meanwhile.
func hostSelfUpdating(a *domain.SelfUpdateAttempt) bool {
	return a != nil && a.Status == "started" && time.Since(a.At) < remotesvc.OpTimeout
}
