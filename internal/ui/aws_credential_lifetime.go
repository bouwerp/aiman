package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// AWS accepts session lifetimes between 15 minutes and 12 hours.
const (
	minCredentialLifetime = 900
	maxCredentialLifetime = 43200
)

// parseCredentialLifetime reads the lifetime field from the credentials screen. An empty
// value returns 0, which the credential layer reads as DefaultDurationSeconds. Anything
// else must be an integer inside AWS's accepted range — an explicit "0" is rejected rather
// than silently treated as "default", because it reads as a mistake.
func parseCredentialLifetime(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("lifetime must be a whole number of seconds (%d–%d), or empty for the default",
			minCredentialLifetime, maxCredentialLifetime)
	}
	if n < minCredentialLifetime || n > maxCredentialLifetime {
		return 0, fmt.Errorf("lifetime must be between %d and %d seconds (got %d)",
			minCredentialLifetime, maxCredentialLifetime, n)
	}
	return n, nil
}

// formatLifetime renders a configured lifetime compactly for the credentials table.
// Zero means unset, which the credential layer treats as DefaultDurationSeconds, so it is
// displayed as that default rather than as a blank.
func formatLifetime(seconds int) string {
	if seconds <= 0 {
		seconds = awsdelegation.DefaultDurationSeconds
	}
	d := time.Duration(seconds) * time.Second
	hours := int(d.Hours())
	minutes := int(d.Minutes()) - hours*60
	switch {
	case hours == 0:
		return fmt.Sprintf("%dm", minutes)
	case minutes == 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dh%02dm", hours, minutes)
	}
}

// openLifetimeEditor starts editing the lifetime of one profile, pre-filled with its
// current value (blank when unset, so the placeholder default shows through). Profiles with
// no local delegation config have nothing to mint from, so nothing to configure either.
func (m AWSCredentialsModel) openLifetimeEditor(e awsHostEntry) (tea.Model, tea.Cmd) {
	switch {
	case e.del == nil:
		m.message = fmt.Sprintf("Cannot set a lifetime for [%s]: no local delegation config for this profile.", e.remoteProfile)
		return m, nil
	case m.renewing[e.key]:
		m.message = fmt.Sprintf("Wait for %s [%s] to finish before changing its lifetime.", e.userAtHost, e.remoteProfile)
		return m, nil
	}
	m.editingLifetime = true
	m.lifetimeKey = e.key
	if e.del.DurationSeconds > 0 {
		m.lifetimeInput.SetValue(strconv.Itoa(e.del.DurationSeconds))
	} else {
		m.lifetimeInput.SetValue("")
	}
	m.lifetimeInput.CursorEnd()
	m.lifetimeInput.Focus()
	m.message = fmt.Sprintf("Credential lifetime for %s [%s] — Enter to save.", e.userAtHost, e.remoteProfile)
	return m, textinput.Blink
}

// closeLifetimeEditor tears down the editor and reports why.
func (m AWSCredentialsModel) closeLifetimeEditor(message string) (tea.Model, tea.Cmd) {
	m.editingLifetime = false
	m.lifetimeKey = ""
	m.lifetimeInput.Blur()
	m.message = message
	return m, nil
}

// updateLifetimeEditor handles keys while the lifetime editor is open. Invalid values leave
// the editor open with an explanation rather than discarding what was typed.
func (m AWSCredentialsModel) updateLifetimeEditor(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return m.closeLifetimeEditor("Lifetime edit cancelled.")
	case "enter":
		entry := m.entryByKey(m.lifetimeKey)
		if entry == nil {
			return m.closeLifetimeEditor("Lifetime target disappeared.")
		}
		seconds, err := parseCredentialLifetime(m.lifetimeInput.Value())
		if err != nil {
			m.message = "✗ " + err.Error()
			return m, nil
		}
		if err := setDelegationLifetime(m.cfg, entry, seconds); err != nil {
			m.message = fmt.Sprintf("✗ Could not save lifetime for %s [%s]: %v",
				entry.userAtHost, entry.remoteProfile, err)
			return m, nil
		}
		return m.closeLifetimeEditor(fmt.Sprintf(
			"Lifetime for %s [%s] set to %s — takes effect on next renew (r or shift+R).",
			entry.userAtHost, entry.remoteProfile, formatLifetime(seconds)))
	}
	var cmd tea.Cmd
	m.lifetimeInput, cmd = m.lifetimeInput.Update(msg)
	return m, cmd
}

// setDelegationLifetime writes seconds to the delegation backing entry and persists the
// config. The delegation is located by SSH target plus profile name, the same matching
// renameManagedDelegationProfile uses, so a remote with several profiles only has the
// selected one changed. A failed save is rolled back so the in-memory config never
// disagrees with what is on disk.
func setDelegationLifetime(cfg *config.Config, entry *awsHostEntry, seconds int) error {
	if entry == nil {
		return fmt.Errorf("no profile selected")
	}
	if entry.del == nil {
		return fmt.Errorf("no local delegation config for profile %q — add it under Manage Remote Servers", entry.remoteProfile)
	}
	if cfg == nil {
		return fmt.Errorf("no configuration loaded")
	}

	target := normalizeAWSProfileName(entry.remoteProfile)
	var updated []*config.AWSDelegation
	var previous []int

	for i := range cfg.Remotes {
		r := &cfg.Remotes[i]
		if strings.TrimSpace(r.Host) != strings.TrimSpace(entry.remote.Host) ||
			strings.TrimSpace(r.User) != strings.TrimSpace(entry.remote.User) ||
			strings.TrimSpace(r.Root) != strings.TrimSpace(entry.remote.Root) {
			continue
		}
		for _, d := range r.AllDelegations() {
			if d == nil || normalizeAWSProfileName(d.Profile) != target {
				continue
			}
			updated = append(updated, d)
			previous = append(previous, d.DurationSeconds)
			d.DurationSeconds = seconds
		}
	}

	if len(updated) == 0 {
		return fmt.Errorf("profile %q is not in local settings for %s", entry.remoteProfile, entry.userAtHost)
	}

	if err := cfg.Save(); err != nil {
		for i, d := range updated {
			d.DurationSeconds = previous[i]
		}
		return err
	}
	return nil
}
