package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/usage"
	"github.com/bouwerp/aiman/internal/usecase"
)

// usageFetchMinInterval stops a selection change from refetching a report
// that was just loaded. A usage event forces a refresh anyway.
const usageFetchMinInterval = 10 * time.Second

// usageMsg is the result of usage.get for one session.
type usageMsg struct {
	sessionID string
	rep       usage.Report
	budget    usage.Budget
	err       error
}

// fetchSessionUsageCmd loads one session's priced rollup from aiman serve.
// The laptop's current offset is the zone the day window is reported in.
func fetchSessionUsageCmd(cfg *config.Config, s domain.Session) tea.Cmd {
	return func() tea.Msg {
		remote, ok := usageRemote(cfg, s)
		if !ok {
			return usageMsg{sessionID: s.ID, err: fmt.Errorf("no remote found")}
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, offsetSec := time.Now().Zone()
		raw, err := usecase.ServeRPC(ctx, mgr, "usage.get", struct {
			Session         string `json:"session"`
			TZOffsetMinutes int    `json:"tz_offset_minutes"`
		}{
			Session:         s.ID,
			TZOffsetMinutes: offsetSec / 60,
		})
		if err != nil {
			return usageMsg{sessionID: s.ID, err: err}
		}
		var body struct {
			Usage  usage.Report `json:"usage"`
			Budget usage.Budget `json:"budget"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return usageMsg{sessionID: s.ID, err: fmt.Errorf("decode usage: %w", err)}
		}
		return usageMsg{sessionID: s.ID, rep: body.Usage, budget: body.Budget}
	}
}

func usageRemote(cfg *config.Config, s domain.Session) (config.Remote, bool) {
	if s.RemoteHost != "" {
		return remoteForHost(cfg, s.RemoteHost)
	}
	return resolveRemote(cfg, s)
}

// maybeFetchUsage starts usage.get unless one is already in flight.
// force skips the interval. It does not skip a fetch that has not returned.
func (m *Model) maybeFetchUsage(s domain.Session, force bool) tea.Cmd {
	if s.ID == "" {
		return nil
	}
	if m.usageInFlight[s.ID] {
		return nil
	}
	if !force {
		if at := m.usageFetchedAt[s.ID]; !at.IsZero() && time.Since(at) < usageFetchMinInterval {
			return nil
		}
	}
	if m.usageInFlight == nil {
		m.usageInFlight = map[string]bool{}
	}
	if m.usageFetchedAt == nil {
		m.usageFetchedAt = map[string]time.Time{}
	}
	m.usageInFlight[s.ID] = true
	m.usageFetchedAt[s.ID] = time.Now()
	return fetchSessionUsageCmd(m.cfg, s)
}

// usageEventFetch refreshes the selected session. For any other session
// it drops the throttle so the next select loads a fresh report.
func (m *Model) usageEventFetch(id string) tea.Cmd {
	if id == "" {
		return nil
	}
	if !m.sessionIsSelected(id) {
		delete(m.usageFetchedAt, id)
		return nil
	}
	s, ok := m.sessionByID(id)
	if !ok {
		return nil
	}
	return m.maybeFetchUsage(s, true)
}

// applyUsageToExtras copies a report onto the cost fields.
// Reviewing and WatchingPR are left unchanged.
// Known false clears cost, tokens, cache, and the context gauge so a stale figure does not linger.
func applyUsageToExtras(x detailMetaExtras, rep usage.Report, budget ...usage.Budget) detailMetaExtras {
	x.HasCost = false
	x.HasCache = false
	x.CostUSD = 0
	x.Tokens = 0
	x.CacheRatio = 0
	x.ContextPct = 0
	x.ContextWindow = 0
	x.BudgetPct = 0
	x.BudgetState = ""
	if len(budget) > 0 {
		x.BudgetPct = budget[0].Pct
		x.BudgetState = budget[0].State
	}
	if !rep.Known {
		return x
	}
	counts := rep.Total.Tokens
	x.Tokens = counts.Input + counts.Output + counts.CacheRead + counts.CacheCreation()
	if rep.Total.Priced {
		x.HasCost = true
		x.CostUSD = rep.Total.CostUSD
	}
	if rep.Total.CacheHitRatio != nil {
		x.HasCache = true
		x.CacheRatio = *rep.Total.CacheHitRatio
	}
	if rep.ContextWindow > 0 {
		x.ContextWindow = rep.ContextWindow
		x.ContextPct = rep.ContextPct
	}
	return x
}

func (m *Model) applyUsageMsg(msg usageMsg) tea.Cmd {
	delete(m.usageInFlight, msg.sessionID)
	if msg.err != nil {
		m.log("usage %s: %v", msg.sessionID, msg.err)
		return nil
	}
	if m.metaExtras == nil {
		m.metaExtras = map[string]detailMetaExtras{}
	}
	prev := m.metaExtras[msg.sessionID]
	m.metaExtras[msg.sessionID] = applyUsageToExtras(prev, msg.rep, msg.budget)
	if msg.budget.State == "over" && prev.BudgetState != "over" {
		return m.showToast("token budget exceeded", true, 4*time.Second)
	}
	return nil
}
