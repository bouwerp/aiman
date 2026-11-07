package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/notify"
)

// NotifyCursorFileName is the gateway's notify.since cursor.
const NotifyCursorFileName = "gateway-notify-cursor"

type expoMessage struct {
	To    string            `json:"to"`
	Title string            `json:"title"`
	Body  string            `json:"body,omitempty"`
	Sound string            `json:"sound,omitempty"`
	Data  map[string]string `json:"data,omitempty"`
}

// SocketSource reads notifications from the serve socket via notify.since.
type SocketSource struct {
	Socket string
}

func (s SocketSource) Since(ctx context.Context, after int64, limit int) ([]domain.Notification, int64, error) {
	params := map[string]any{"after": after, "limit": limit}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, 0, err
	}
	resp, err := serveClient(s.Socket).CallRaw("notify.since", raw)
	if err != nil {
		return nil, 0, err
	}
	if resp.Error != nil {
		return nil, 0, errors.New(resp.Error.Message)
	}
	body, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, 0, err
	}
	var res struct {
		Type    string `json:"type"`
		Entries []struct {
			Seq       int64  `json:"seq"`
			SessionID string `json:"session_id"`
			Kind      string `json:"kind"`
			Label     string `json:"label"`
			Host      string `json:"host"`
			Text      string `json:"text"`
			At        string `json:"at"`
		} `json:"entries"`
		LastSeq int64 `json:"last_seq"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, 0, err
	}
	out := make([]domain.Notification, 0, len(res.Entries))
	for _, e := range res.Entries {
		out = append(out, domain.Notification{
			Seq:       e.Seq,
			SessionID: e.SessionID,
			Kind:      domain.NotifyKind(e.Kind),
			Label:     e.Label,
			Host:      e.Host,
			Text:      e.Text,
		})
	}
	return out, res.LastSeq, nil
}

// ExpoSender delivers a batch of push messages.
type ExpoSender func(ctx context.Context, msgs []expoMessage) error

// NewPhoneSink creates a phone push sink for the gateway on host (the OS
// hostname, the same one `aiman-gateway pair` reports). send is usually
// (*ExpoClient).Send.
func NewPhoneSink(store *PushStore, host string, send ExpoSender) notify.Sink {
	return &phoneSink{store: store, host: host, send: send}
}

type phoneSink struct {
	store *PushStore
	host  string
	send  ExpoSender
}

func (p *phoneSink) Name() string { return "phone" }

func phoneStateFor(k domain.NotifyKind) string {
	switch k {
	case domain.NotifyWaitingInput, domain.NotifyCIEscalation, domain.NotifyReviewDone:
		return stateWaitingInput
	case domain.NotifyEnded:
		return stateExited
	case domain.NotifyError:
		return stateErrored
	default:
		return stateWaitingInput
	}
}

func (p *phoneSink) Send(ctx context.Context, n domain.Notification) error {
	// Phone pushes open a session; an agent update belongs to no session.
	if p == nil || p.store == nil || n.Kind == domain.NotifyAgentUpdate {
		return nil
	}
	ps := phoneStateFor(n.Kind)
	devices := p.store.Devices()
	msgs := make([]expoMessage, 0)
	for _, d := range devices {
		if !wantsState(d, ps) {
			continue
		}
		title := notifyTitle(ps)
		switch n.Kind {
		case domain.NotifyCIEscalation:
			title = "Aiman: CI needs you"
		case domain.NotifyReviewDone:
			title = "Aiman: review done"
		default:
		}
		msgs = append(msgs, expoMessage{
			To:    d.Token,
			Title: title,
			Body:  n.Text,
			Sound: "default",
			Data:  pushData(p.host, n.SessionID),
		})
	}
	if len(msgs) == 0 || p.send == nil {
		return nil
	}
	return p.send(ctx, msgs)
}

// pushData is what the app reads when a push is opened: the host and
// session, and the deep link that opens them.
func pushData(host, session string) map[string]string {
	return map[string]string{
		"session": session,
		"host":    host,
		"url":     "aimanphone://h/" + url.PathEscape(host) + "/s/" + url.PathEscape(session),
	}
}

func notifyTitle(state string) string {
	switch state {
	case stateWaitingInput:
		return "Aiman needs input"
	case stateExited:
		return "Aiman session exited"
	case stateErrored:
		return "Aiman session errored"
	default:
		return "Aiman session update"
	}
}
