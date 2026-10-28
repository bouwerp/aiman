package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/notify"
	"github.com/bouwerp/aiman/internal/server"
)

const (
	expoPushURL          = "https://exp.host/--/api/v2/push/send"
	deepLinkScheme       = "aimanphone://session/"
	NotifyCursorFileName = "gateway-notify-cursor"
)

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
	resp, err := server.CallRaw(s.Socket, "notify.since", raw)
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

// NewPhoneSink creates a phone push sink.
func NewPhoneSink(store *PushStore, sender func([]expoMessage) error) notify.Sink {
	if sender == nil {
		sender = sendExpo
	}
	return &phoneSink{store: store, sender: sender}
}

type phoneSink struct {
	store  *PushStore
	sender func([]expoMessage) error
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
			Data:  map[string]string{"url": deepLinkScheme + n.SessionID},
		})
	}
	if len(msgs) == 0 {
		return nil
	}
	return p.sender(msgs)
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

func sendExpo(msgs []expoMessage) error {
	if len(msgs) == 0 {
		return nil
	}
	payload, err := json.Marshal(msgs)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, expoPushURL, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return strconv.ErrSyntax
	}
	return nil
}

// Legacy types for compatibility with old tests.
type Notifier struct {
	store  *PushStore
	sender func([]expoMessage) error
	now    func() time.Time
	mu     sync.Mutex
	last   map[string]string
	sentAt map[string]time.Time
	known  map[string]bool
}

func NewNotifier(store *PushStore, sender func([]expoMessage) error) *Notifier {
	if sender == nil {
		sender = sendExpo
	}
	n := &Notifier{store: store, sender: sender, now: time.Now, last: map[string]string{}, sentAt: map[string]time.Time{}, known: map[string]bool{}}
	return n
}

func (n *Notifier) anyWants(state string) bool {
	if n == nil || n.store == nil {
		return false
	}
	for _, d := range n.store.Devices() {
		if wantsState(d, state) {
			return true
		}
	}
	return false
}

func (n *Notifier) Observe(id, state, message string) {
	if n == nil || n.store == nil || id == "" {
		return
	}
	ps := state
	if ps == "idle" {
		ps = stateExited
	}
	switch ps {
	case "exited":
		ps = stateExited
	case "errored":
		ps = stateErrored
	case "waiting_input":
		ps = stateWaitingInput
	case "working":
		ps = stateWorking
	}
	n.mu.Lock()
	prev := n.last[id]
	n.known[id] = true
	now := n.now()
	lastSent, hasSent := n.sentAt[id]
	recently := hasSent
	n.mu.Unlock()
	if prev == ps {
		if !hasSent && n.anyWants(ps) {
			// allow
		} else {
			return
		}
	}
	if !n.anyWants(ps) {
		n.mu.Lock()
		n.last[id] = ps
		n.mu.Unlock()
		return
	}
	if prev == "" {
		// first observation - old behavior notified on first transition if wants it
	} else if recently && now.Sub(lastSent) < 30*time.Second {
		n.mu.Lock()
		n.last[id] = ps
		n.mu.Unlock()
		return
	}
	// proceed
	n.mu.Lock()
	n.last[id] = ps
	n.mu.Unlock()
	if recently && now.Sub(lastSent) < 30*time.Second {
		return
	}
	msgs := []expoMessage{}
	title := notifyTitle(ps)
	body := message
	if body == "" {
		body = id
	}
	for _, d := range n.store.Devices() {
		if !wantsState(d, ps) {
			continue
		}
		msgs = append(msgs, expoMessage{
			To:    d.Token,
			Title: title,
			Body:  body,
			Sound: "default",
			Data:  map[string]string{"url": deepLinkScheme + id},
		})
	}
	if len(msgs) == 0 {
		return
	}
	if err := n.sender(msgs); err != nil {
		return
	}
	n.mu.Lock()
	n.sentAt[id] = now
	n.mu.Unlock()
}
