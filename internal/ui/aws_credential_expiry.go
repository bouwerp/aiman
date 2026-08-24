package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	tea "github.com/charmbracelet/bubbletea"
)

// awsCredExpiryWarnWindow is how close to expiry delegated credentials must be before the
// main dashboard warns about them.
const awsCredExpiryWarnWindow = 15 * time.Minute

// awsCredExpiryPollInterval is how often the dashboard re-reads remote expiry times. The
// countdown itself is recomputed on every render, so this only has to be frequent enough
// to notice credentials pushed by another process.
const awsCredExpiryPollInterval = 5 * time.Minute

type expiryUrgency int

const (
	expiryUnknown expiryUrgency = iota
	expiryOK
	expiryWarn
	expiryExpired
)

// urgencyOf classifies how urgently a credential needs renewing.
func urgencyOf(at time.Time, now time.Time) expiryUrgency {
	if at.IsZero() {
		return expiryUnknown
	}
	remaining := at.Sub(now)
	switch {
	case remaining <= 0:
		return expiryExpired
	case remaining <= awsCredExpiryWarnWindow:
		return expiryWarn
	default:
		return expiryOK
	}
}

// formatExpiresIn renders a time-to-expiry for the credentials table: "4h12m", "42m",
// "expired", or "—" when unknown. Values derived from the credentials file's mtime rather
// than a recorded expiry are prefixed with "~".
func formatExpiresIn(at time.Time, approx bool, now time.Time) string {
	if at.IsZero() {
		return "—"
	}
	remaining := at.Sub(now)
	if remaining <= 0 {
		return "expired"
	}
	prefix := ""
	if approx {
		prefix = "~"
	}
	if remaining < time.Minute {
		return prefix + "<1m"
	}
	if remaining < time.Hour {
		return fmt.Sprintf("%s%dm", prefix, int(remaining.Minutes()))
	}
	hours := int(remaining.Hours())
	minutes := int(remaining.Minutes()) - hours*60
	return fmt.Sprintf("%s%dh%02dm", prefix, hours, minutes)
}

// awsCredExpiryItem is one remote profile's expiry, as tracked by the main dashboard.
type awsCredExpiryItem struct {
	userAtHost string
	profile    string
	expiresAt  time.Time
	approx     bool
}

// --- dashboard messages ---

// awsCredExpiryTickMsg drives the periodic expiry poll.
type awsCredExpiryTickMsg time.Time

// awsCredExpiryPollMsg carries the result of one expiry poll across all remotes.
type awsCredExpiryPollMsg struct {
	items []awsCredExpiryItem
}

// awsCredBulkRenewMsg reports the outcome of a refresh-all triggered outside the
// credentials screen (shift+R on the dashboard).
type awsCredBulkRenewMsg struct {
	renewed  int
	failures []string
}

func tickAWSCredExpiry() tea.Cmd {
	return tea.Tick(awsCredExpiryPollInterval, func(t time.Time) tea.Msg {
		return awsCredExpiryTickMsg(t)
	})
}

func filterExpiryItemsByAllowlist(items []awsCredExpiryItem, cfg *config.Config) []awsCredExpiryItem {
	if cfg == nil || cfg.AWS.IncludeProfiles == nil {
		return items
	}
	allowed := map[string]bool{}
	for _, t := range syncedDelegations(cfg) {
		allowed[t.userAtHost+"\x00"+t.profile] = true
	}
	var out []awsCredExpiryItem
	for _, it := range items {
		if allowed[it.userAtHost+"\x00"+it.profile] {
			out = append(out, it)
		}
	}
	return out
}

// formatAWSCredExpiryBanner returns the dashboard warning line, or "" when nothing is
// close to expiry. It names the most urgent profile and counts the rest.
func formatAWSCredExpiryBanner(items []awsCredExpiryItem, now time.Time) string {
	var urgent []awsCredExpiryItem
	for _, it := range items {
		switch urgencyOf(it.expiresAt, now) {
		case expiryWarn, expiryExpired:
			urgent = append(urgent, it)
		}
	}
	if len(urgent) == 0 {
		return ""
	}
	sort.Slice(urgent, func(i, j int) bool { return urgent[i].expiresAt.Before(urgent[j].expiresAt) })

	first := urgent[0]
	remaining := formatExpiresIn(first.expiresAt, first.approx, now)
	var detail string
	if urgencyOf(first.expiresAt, now) == expiryExpired {
		detail = fmt.Sprintf("AWS credentials expired: %s [%s]", first.userAtHost, first.profile)
	} else {
		detail = fmt.Sprintf("AWS credentials expire in %s: %s [%s]", remaining, first.userAtHost, first.profile)
	}
	if len(urgent) > 1 {
		detail += fmt.Sprintf(" (+%d more)", len(urgent)-1)
	}
	return detail + " — shift+R to refresh all"
}

// syncedDelegations returns every (remote, delegation) pair with credential syncing
// enabled, which is the set of profiles aiman can mint and push credentials for.
func syncedDelegations(cfg *config.Config) []delegationTarget {
	var out []delegationTarget
	if cfg == nil {
		return out
	}
	for _, r := range cfg.Remotes {
		userAtHost := r.Host
		if r.User != "" {
			userAtHost = r.User + "@" + r.Host
		}
		for _, d := range r.AllDelegations() {
			if d == nil || !d.SyncCredentials {
				continue
			}
			if !cfg.AWSLocalProfileAllowed(d.LocalSourceProfile()) {
				continue
			}
			profile := strings.TrimSpace(d.Profile)
			if profile == "" {
				profile = "default"
			}
			out = append(out, delegationTarget{
				userAtHost: userAtHost,
				remote:     r,
				profile:    profile,
				del:        d,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].userAtHost != out[j].userAtHost {
			return out[i].userAtHost < out[j].userAtHost
		}
		return out[i].profile < out[j].profile
	})
	return out
}

// delegationTarget is one profile on one remote that aiman manages credentials for.
type delegationTarget struct {
	userAtHost string
	remote     config.Remote
	profile    string
	del        *config.AWSDelegation
}

// pollAWSCredExpiryCmd reads expiry times for every synced profile. One SSH round trip
// per host; hosts that cannot be reached contribute nothing rather than failing the poll.
func pollAWSCredExpiryCmd(cfg *config.Config) tea.Cmd {
	targets := syncedDelegations(cfg)
	if len(targets) == 0 {
		return nil
	}
	return func() tea.Msg {
		byHost := map[string][]delegationTarget{}
		var hostOrder []string
		for _, t := range targets {
			if _, ok := byHost[t.userAtHost]; !ok {
				hostOrder = append(hostOrder, t.userAtHost)
			}
			byHost[t.userAtHost] = append(byHost[t.userAtHost], t)
		}

		var items []awsCredExpiryItem
		for _, host := range hostOrder {
			group := byHost[host]
			mgr := ssh.NewManager(ssh.Config{
				Host: group[0].remote.Host,
				User: group[0].remote.User,
				Root: group[0].remote.Root,
			})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			info, err := awsdelegation.ReadCredentialExpirations(ctx, mgr)
			cancel()
			if err != nil {
				continue
			}
			for _, t := range group {
				at, approx, ok := info.For(t.profile, t.del.DurationSeconds)
				if !ok {
					continue
				}
				items = append(items, awsCredExpiryItem{
					userAtHost: t.userAtHost,
					profile:    t.profile,
					expiresAt:  at,
					approx:     approx,
				})
			}
		}
		return awsCredExpiryPollMsg{items: items}
	}
}

// renewAllDelegatedCredentialsCmd mints and pushes fresh credentials for every synced
// profile, regardless of current status. Profiles on the same host are handled
// sequentially because they share ~/.aws/credentials.
func renewAllDelegatedCredentialsCmd(cfg *config.Config) tea.Cmd {
	targets := syncedDelegations(cfg)
	if len(targets) == 0 {
		return func() tea.Msg {
			return awsCredBulkRenewMsg{failures: []string{"no remotes with sync_credentials enabled"}}
		}
	}
	return func() tea.Msg {
		byHost := map[string][]delegationTarget{}
		var hostOrder []string
		for _, t := range targets {
			if _, ok := byHost[t.userAtHost]; !ok {
				hostOrder = append(hostOrder, t.userAtHost)
			}
			byHost[t.userAtHost] = append(byHost[t.userAtHost], t)
		}

		result := awsCredBulkRenewMsg{}
		for _, host := range hostOrder {
			group := byHost[host]
			mgr := ssh.NewManager(ssh.Config{
				Host: group[0].remote.Host,
				User: group[0].remote.User,
				Root: group[0].remote.Root,
			})
			timeout := time.Duration(len(group)+1) * 90 * time.Second
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			for _, t := range group {
				if _, err := pushFreshCredentials(ctx, mgr, t.del, t.profile); err != nil {
					result.failures = append(result.failures, fmt.Sprintf("%s [%s]: %v", t.userAtHost, t.profile, err))
					continue
				}
				result.renewed++
			}
			cancel()
		}
		return result
	}
}
