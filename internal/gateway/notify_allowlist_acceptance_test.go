package gateway

import (
	"context"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// phoneSinkSent sends one notification of kind to a device that wants every
// phone state and returns how many messages reached the sender.
func phoneSinkSent(t *testing.T, kind domain.NotifyKind) int {
	t.Helper()
	store := mustStore(t)
	if err := store.Register(PushDevice{
		Token: "ExponentPushToken[a]", DeviceID: "d1",
		States: []string{stateWaitingInput, stateExited, stateErrored},
	}); err != nil {
		t.Fatal(err)
	}
	var sent []expoMessage
	sink := NewPhoneSink(store, "devbox", func(_ context.Context, m []expoMessage) error {
		sent = append(sent, m...)
		return nil
	})
	if err := sink.Send(context.Background(), domain.Notification{SessionID: "s1", Kind: kind, Text: "x"}); err != nil {
		t.Fatalf("Send(%s): %v", kind, err)
	}
	return len(sent)
}

func TestPhoneSinkSkipsHostLevelKinds(t *testing.T) {
	for _, kind := range []domain.NotifyKind{domain.NotifyHostUpdate, domain.NotifyAgentUpdate} {
		if n := phoneSinkSent(t, kind); n != 0 {
			t.Errorf("kind %s pushed %d messages, want none", kind, n)
		}
	}
}

func TestPhoneSinkStillPushesWaitingInput(t *testing.T) {
	if n := phoneSinkSent(t, domain.NotifyWaitingInput); n != 1 {
		t.Fatalf("waiting_input pushed %d messages, want 1", n)
	}
}

// A kind a newer serve adds stays silent on this gateway instead of reading
// as "needs input".
func TestPhoneSinkSkipsUnknownKind(t *testing.T) {
	if n := phoneSinkSent(t, domain.NotifyKind("future_kind")); n != 0 {
		t.Fatalf("future_kind pushed %d messages, want none", n)
	}
}

// pushKinds is exactly the set phoneStateFor names.
func TestPushKindsMatchPhoneStates(t *testing.T) {
	want := []domain.NotifyKind{
		domain.NotifyWaitingInput,
		domain.NotifyCIEscalation,
		domain.NotifyReviewDone,
		domain.NotifyEnded,
		domain.NotifyError,
	}
	if len(pushKinds) != len(want) {
		t.Errorf("pushKinds = %v, want exactly %v", pushKinds, want)
	}
	for _, k := range want {
		if !pushKinds[k] {
			t.Errorf("pushKinds misses %s", k)
		}
	}
	for _, k := range []domain.NotifyKind{domain.NotifyHostUpdate, domain.NotifyAgentUpdate} {
		if pushKinds[k] {
			t.Errorf("pushKinds holds %s", k)
		}
	}
	for _, k := range want {
		if n := phoneSinkSent(t, k); n != 1 {
			t.Errorf("kind %s pushed %d messages, want 1", k, n)
		}
	}
}
