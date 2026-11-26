package feed

import (
	"context"
	"slices"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

const defaultWakeTTL = 24 * time.Hour

func dismiss(ctx context.Context, d ActDeps, a domain.FeedAction, req ActRequest) (domain.FeedAction, error) {
	reason := req.Reason
	if reason == "" {
		reason = domain.FeedNotRelevant
	}
	now := d.now()
	state := ActionDismissed
	dis := &domain.FeedDismissal{Reason: reason, Note: req.Note}
	var mutes []domain.FeedMute
	var holds map[string]int64
	switch reason {
	case domain.FeedNotRelevant:
	case domain.FeedLater:
		if req.Until.IsZero() || !req.Until.After(now) {
			return a, refuse("later needs an until in the future")
		}
		state, dis.Until = ActionSnoozed, req.Until
	case domain.FeedHandled, domain.FeedMuteThread, domain.FeedMuteSender, domain.FeedMuteChannel:
		evs, err := answered(ctx, d, a)
		if err != nil {
			return a, err
		}
		if reason == domain.FeedHandled {
			holds = heldThreads(evs)
			break
		}
		mutes, err = muteSet(d, a.ID, reason, evs, now)
		if err != nil {
			return a, err
		}
	default:
		return a, refuse("unknown dismiss reason %q", reason)
	}
	for _, m := range mutes {
		if d.Mutes == nil {
			return a, refuse("mutes are not available on this host")
		}
		if err := d.Mutes.Mute(ctx, m); err != nil {
			return a, err
		}
	}
	for key, upTo := range holds {
		if err := d.Store.HoldFeedThread(ctx, key, upTo); err != nil {
			return a, err
		}
	}
	a.State, a.Dismissal, a.DecidedBy, a.DecidedAt = state, dis, req.By, now
	if err := d.Store.UpdateFeedAction(ctx, a); err != nil {
		return a, err
	}
	d.changed(a)
	return a, nil
}

func answered(ctx context.Context, d ActDeps, a domain.FeedAction) ([]domain.FeedEvent, error) {
	if d.Events == nil || len(a.Answers) == 0 {
		return nil, nil
	}
	return d.Events.ByIDs(ctx, a.Answers)
}

// heldThreads maps each answered thread to its newest answered event id; the
// thread stays quiet until a newer event arrives.
func heldThreads(evs []domain.FeedEvent) map[string]int64 {
	out := map[string]int64{}
	for _, e := range evs {
		if e.ThreadKey == "" || e.ID <= 0 {
			continue
		}
		out[e.ThreadKey] = max(out[e.ThreadKey], e.ID)
	}
	return out
}

// sessionActor is an event written by aiman's own sessions: never muted.
func sessionActor(e domain.FeedEvent) bool {
	return e.Class == "session" || e.Source == "aiman"
}

func muteSet(d ActDeps, actionID int64, reason domain.FeedDismissReason, evs []domain.FeedEvent, now time.Time) ([]domain.FeedMute, error) {
	days := d.MuteDays
	if days <= 0 {
		days = 7
	}
	until := now.Add(time.Duration(days) * 24 * time.Hour)
	var scope domain.FeedMuteScope
	var keys []string
	for _, e := range evs {
		if sessionActor(e) {
			continue
		}
		switch reason {
		case domain.FeedMuteThread:
			scope, keys = domain.FeedMuteScope("thread"), append(keys, e.ThreadKey)
		case domain.FeedMuteSender:
			scope, keys = domain.FeedMuteScope("sender"), append(keys, e.Actor)
		case domain.FeedMuteChannel:
			if e.Kind == DigestKind {
				scope, keys = domain.FeedMuteScope("channel"), append(keys, e.Channel)
			}
		}
	}
	keys = slices.DeleteFunc(keys, func(k string) bool { return k == "" })
	slices.Sort(keys)
	keys = slices.Compact(keys)
	if len(keys) == 0 {
		return nil, refuse("%s has nothing to mute on this action", reason)
	}
	out := make([]domain.FeedMute, len(keys))
	for i, k := range keys {
		out[i] = domain.FeedMute{Scope: scope, Key: k, Until: until, ActionID: actionID}
	}
	return out, nil
}

// WakeSnoozed turns snoozed actions whose time has come back into proposed
// ones and returns how many it woke.
func WakeSnoozed(ctx context.Context, d ActDeps) (int, error) {
	actMu.Lock()
	defer actMu.Unlock()
	acts, err := d.Store.ListFeedActions(ctx, []domain.FeedActionState{ActionSnoozed}, listAll)
	if err != nil {
		return 0, err
	}
	now := d.now()
	ttl := d.TTL
	if ttl <= 0 {
		ttl = defaultWakeTTL
	}
	n := 0
	for _, a := range acts {
		if a.Dismissal == nil || a.Dismissal.Until.After(now) {
			continue
		}
		a.State, a.Dismissal, a.DecidedBy, a.DecidedAt = ActionProposed, nil, "", time.Time{}
		if a.ExpiresAt.Before(now.Add(ttl)) {
			a.ExpiresAt = now.Add(ttl)
		}
		if err := d.Store.UpdateFeedAction(ctx, a); err != nil {
			return n, err
		}
		d.changed(a)
		n++
	}
	return n, nil
}
