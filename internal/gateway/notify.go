package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/server"
)

const (
	expoPushURL    = "https://exp.host/--/api/v2/push/send"
	coalesceWindow = 30 * time.Second
	watchPollEvery = 2 * time.Second
	deepLinkScheme = "aimanphone://session/"
)

type expoMessage struct {
	To    string            `json:"to"`
	Title string            `json:"title"`
	Body  string            `json:"body,omitempty"`
	Sound string            `json:"sound,omitempty"`
	Data  map[string]string `json:"data,omitempty"`
}

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
	return &Notifier{
		store:  store,
		sender: sender,
		now:    time.Now,
		last:   map[string]string{},
		sentAt: map[string]time.Time{},
		known:  map[string]bool{},
	}
}

// Observe records a session's agent state and notifies on a subscribed transition.
func (n *Notifier) Observe(id, state, message string) {
	if n == nil || id == "" {
		return
	}
	n.mu.Lock()
	prev := n.last[id]
	n.last[id] = state
	n.known[id] = true
	now := n.now()
	lastSent, recently := n.sentAt[id]
	n.mu.Unlock()
	if prev == state || !n.anyWants(state) {
		return
	}
	if recently && now.Sub(lastSent) < coalesceWindow {
		return
	}
	msgs := n.messages(id, state, message)
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

func (n *Notifier) anyWants(state string) bool {
	for _, d := range n.store.Devices() {
		if wantsState(d, state) {
			return true
		}
	}
	return false
}

func (n *Notifier) messages(id, state, message string) []expoMessage {
	title := notifyTitle(state)
	body := message
	if body == "" {
		body = id
	}
	url := deepLinkScheme + id
	var msgs []expoMessage
	for _, d := range n.store.Devices() {
		if !wantsState(d, state) {
			continue
		}
		msgs = append(msgs, expoMessage{
			To:    d.Token,
			Title: title,
			Body:  body,
			Sound: "default",
			Data:  map[string]string{"url": url},
		})
	}
	return msgs
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

// Watch polls session.list until ctx is done and feeds Observe.
func (n *Notifier) Watch(ctx context.Context, socket string) {
	n.scan(socket)
	tick := time.NewTicker(watchPollEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			n.scan(socket)
		}
	}
}

func (n *Notifier) scan(socket string) {
	resp, err := server.CallRaw(socket, "session.list", json.RawMessage("{}"))
	if err != nil || resp.Error != nil {
		return
	}
	body, err := json.Marshal(resp.Result)
	if err != nil {
		return
	}
	var list struct {
		Sessions []struct {
			ID           string `json:"id"`
			State        string `json:"state"`
			StateMessage string `json:"state_message"`
			Name         string `json:"name"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return
	}
	live := map[string]bool{}
	for _, sess := range list.Sessions {
		if sess.ID == "" {
			continue
		}
		live[sess.ID] = true
		msg := sess.StateMessage
		if msg == "" {
			msg = sess.Name
		}
		n.Observe(sess.ID, sess.State, msg)
	}
	n.mu.Lock()
	var gone []string
	for id := range n.known {
		if !live[id] {
			gone = append(gone, id)
		}
	}
	n.mu.Unlock()
	for _, id := range gone {
		n.Observe(id, stateExited, "")
		n.mu.Lock()
		delete(n.known, id)
		n.mu.Unlock()
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, expoPushURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("expo push: HTTP %d", resp.StatusCode)
	}
	return nil
}
