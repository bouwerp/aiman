package server

import "sync"

// hubBuffer is how many events one subscriber may fall behind by before it
// starts losing them.
const hubBuffer = 64

// Hub is where request handlers announce changes that event-stream clients
// should hear about without polling: todo_changed, steer, and the reserved
// usage, notify and verify types.
type Hub interface {
	Publish(ev SessionEvent)
}

// eventHub fans each published event out to every live session.events stream.
// Publishing never blocks: a subscriber whose buffer is full misses the event,
// since a stalled laptop connection must not hold up a socket handler.
type eventHub struct {
	mu   sync.Mutex
	subs map[chan SessionEvent]struct{}
}

func newEventHub() *eventHub {
	return &eventHub{subs: map[chan SessionEvent]struct{}{}}
}

// Publish delivers ev to every subscriber with room for it.
func (h *eventHub) Publish(ev SessionEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Subscribe registers a stream. The returned cancel removes and closes the
// channel; it is safe to call more than once.
func (h *eventHub) Subscribe() (<-chan SessionEvent, func()) {
	ch := make(chan SessionEvent, hubBuffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
}
