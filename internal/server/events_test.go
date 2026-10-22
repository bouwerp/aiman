package server

import (
	"testing"
	"time"
)

// Publishers run on request paths, so a subscriber that stops reading must cost
// it events, never stall the publisher.
func TestHubFansOutAndDropsWhenFull(t *testing.T) {
	h := newEventHub()
	a, cancelA := h.Subscribe()
	defer cancelA()
	b, cancelB := h.Subscribe()
	defer cancelB()

	h.Publish(SessionEvent{Type: "todo_changed", ID: "x", Rev: 1})
	for name, ch := range map[string]<-chan SessionEvent{"a": a, "b": b} {
		select {
		case ev := <-ch:
			if ev.Type != "todo_changed" || ev.Rev != 1 {
				t.Fatalf("%s got %+v", name, ev)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s got nothing", name)
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 65; i++ {
			h.Publish(SessionEvent{Type: "todo_changed", ID: "x", Rev: int64(i)})
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish blocked on a subscriber that is not reading")
	}
}

// After cancel the subscriber is gone: publishing must not panic on a closed
// channel and the channel must be closed so a reader loop ends.
func TestHubCancelUnsubscribes(t *testing.T) {
	h := newEventHub()
	ch, cancel := h.Subscribe()
	cancel()
	cancel()
	h.Publish(SessionEvent{Type: "steer", ID: "x"})
	if _, ok := <-ch; ok {
		t.Fatal("expected the channel to be closed after cancel")
	}
}
