package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

type fakeExpo struct {
	mu          sync.Mutex
	sendStatus  int
	sendBody    string
	receipts    map[string]string // ticket id -> receipt JSON
	receiptReqs [][]string
	sent        [][]expoMessage
}

func (f *fakeExpo) server(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/send":
			var msgs []expoMessage
			if err := json.Unmarshal(body, &msgs); err != nil {
				t.Errorf("send body: %v", err)
			}
			f.sent = append(f.sent, msgs)
			if f.sendStatus != 0 {
				w.WriteHeader(f.sendStatus)
			}
			_, _ = io.WriteString(w, f.sendBody)
		case "/receipts":
			var req struct {
				IDs []string `json:"ids"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("receipts body: %v", err)
			}
			f.receiptReqs = append(f.receiptReqs, req.IDs)
			parts := []string{}
			for _, id := range req.IDs {
				if rc, ok := f.receipts[id]; ok {
					parts = append(parts, `"`+id+`":`+rc)
				}
			}
			_, _ = io.WriteString(w, `{"data":{`+strings.Join(parts, ",")+`}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func newTestExpo(t *testing.T, f *fakeExpo, now *time.Time, tokens ...string) *ExpoClient {
	t.Helper()
	store := mustStore(t)
	for i, tok := range tokens {
		if err := store.Register(PushDevice{Token: tok, DeviceID: "d" + string(rune('0'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	ts := f.server(t)
	c := NewExpoClient(store)
	c.SendURL = ts.URL + "/send"
	c.ReceiptsURL = ts.URL + "/receipts"
	c.HTTP = ts.Client()
	c.now = func() time.Time { return *now }
	return c
}

func storeTokens(s *PushStore) []string {
	var out []string
	for _, d := range s.Devices() {
		out = append(out, d.Token)
	}
	slices.Sort(out)
	return out
}

func msgsTo(tokens ...string) []expoMessage {
	out := make([]expoMessage, 0, len(tokens))
	for _, tok := range tokens {
		out = append(out, expoMessage{To: tok, Title: "t"})
	}
	return out
}

func TestExpoSend(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantErr    string
		wantTokens []string
		wantTicket []string
	}{
		{
			name:       "ok tickets are kept",
			body:       `{"data":[{"status":"ok","id":"t1"},{"status":"ok","id":"t2"}]}`,
			wantTokens: []string{"A", "B"},
			wantTicket: []string{"t1", "t2"},
		},
		{
			name:       "DeviceNotRegistered drops that token",
			body:       `{"data":[{"status":"ok","id":"t1"},{"status":"error","message":"gone","details":{"error":"DeviceNotRegistered"}}]}`,
			wantTokens: []string{"A"},
			wantTicket: []string{"t1"},
		},
		{
			name:       "other ticket errors keep the token",
			body:       `{"data":[{"status":"error","details":{"error":"MessageRateExceeded"}},{"status":"ok","id":"t2"}]}`,
			wantTokens: []string{"A", "B"},
			wantTicket: []string{"t2"},
		},
		{
			name:       "non-2xx is an error with the status",
			status:     http.StatusInternalServerError,
			body:       `oops`,
			wantErr:    "500",
			wantTokens: []string{"A", "B"},
		},
		{
			name:       "request errors",
			body:       `{"errors":[{"code":"PUSH_TOO_MANY_EXPERIENCE_IDS","message":"x"}]}`,
			wantErr:    "PUSH_TOO_MANY_EXPERIENCE_IDS",
			wantTokens: []string{"A", "B"},
		},
		{
			name:       "unparseable 2xx body",
			body:       `not json`,
			wantErr:    "expo push",
			wantTokens: []string{"A", "B"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
			f := &fakeExpo{sendStatus: tt.status, sendBody: tt.body}
			c := newTestExpo(t, f, &now, "A", "B")
			err := c.Send(context.Background(), msgsTo("A", "B"))
			if tt.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err %v want %q", err, tt.wantErr)
			}
			if got := storeTokens(c.store); !slices.Equal(got, tt.wantTokens) {
				t.Fatalf("tokens %v want %v", got, tt.wantTokens)
			}
			if got := c.ticketIDs(); !slices.Equal(got, tt.wantTicket) {
				t.Fatalf("tickets %v want %v", got, tt.wantTicket)
			}
		})
	}
}

func TestExpoSendChunksAt100(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	f := &fakeExpo{sendBody: `{"data":[]}`}
	c := newTestExpo(t, f, &now)
	tokens := make([]string, 150)
	for i := range tokens {
		tokens[i] = "T"
	}
	if err := c.Send(context.Background(), msgsTo(tokens...)); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 2 || len(f.sent[0]) != 100 || len(f.sent[1]) != 50 {
		t.Fatalf("chunks %d", len(f.sent))
	}
}

func TestExpoReceipts(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	f := &fakeExpo{
		sendBody: `{"data":[{"status":"ok","id":"old-ok"},{"status":"ok","id":"old-gone"},{"status":"ok","id":"old-pending"}]}`,
		receipts: map[string]string{
			"old-ok":   `{"status":"ok"}`,
			"old-gone": `{"status":"error","details":{"error":"DeviceNotRegistered"}}`,
		},
	}
	c := newTestExpo(t, f, &now, "A", "B", "C", "D")
	if err := c.Send(context.Background(), msgsTo("A", "B", "C")); err != nil {
		t.Fatal(err)
	}
	f.sendBody = `{"data":[{"status":"ok","id":"young"}]}`
	now = now.Add(10 * time.Minute)
	if err := c.Send(context.Background(), msgsTo("D")); err != nil {
		t.Fatal(err)
	}

	steps := []struct {
		name        string
		advance     time.Duration
		wantQueried []string
		wantTickets []string
		wantTokens  []string
	}{
		{"nothing is old enough yet", 4 * time.Minute, nil, []string{"old-gone", "old-ok", "old-pending", "young"}, []string{"A", "B", "C", "D"}},
		{"old tickets are checked", 2 * time.Minute, []string{"old-gone", "old-ok", "old-pending"}, []string{"old-pending", "young"}, []string{"A", "C", "D"}},
		{"young ticket comes due", 15 * time.Minute, []string{"old-pending", "young"}, []string{"old-pending", "young"}, []string{"A", "C", "D"}},
		{"unanswered tickets expire after a day", 24 * time.Hour, nil, nil, []string{"A", "C", "D"}},
	}
	for _, st := range steps {
		f.receiptReqs = nil
		now = now.Add(st.advance)
		if err := c.CheckReceipts(context.Background()); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		var queried []string
		for _, ids := range f.receiptReqs {
			queried = append(queried, ids...)
		}
		slices.Sort(queried)
		if !slices.Equal(queried, st.wantQueried) {
			t.Fatalf("%s: queried %v want %v", st.name, queried, st.wantQueried)
		}
		if got := c.ticketIDs(); !slices.Equal(got, st.wantTickets) {
			t.Fatalf("%s: tickets %v want %v", st.name, got, st.wantTickets)
		}
		if got := storeTokens(c.store); !slices.Equal(got, st.wantTokens) {
			t.Fatalf("%s: tokens %v want %v", st.name, got, st.wantTokens)
		}
	}
}

func TestPhoneSinkPushData(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		session string
		wantURL string
	}{
		{"plain", "devbox", "s1", "aimanphone://h/devbox/s/s1"},
		{"escaped", "dev box", "a/b", "aimanphone://h/dev%20box/s/a%2Fb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := mustStore(t)
			if err := store.Register(PushDevice{Token: "tok", DeviceID: "d"}); err != nil {
				t.Fatal(err)
			}
			var sent []expoMessage
			sink := NewPhoneSink(store, tt.host, func(_ context.Context, m []expoMessage) error {
				sent = append(sent, m...)
				return nil
			})
			if err := sink.Send(context.Background(), domain.Notification{SessionID: tt.session, Kind: domain.NotifyWaitingInput, Text: "x"}); err != nil {
				t.Fatal(err)
			}
			if len(sent) != 1 {
				t.Fatalf("sent %d", len(sent))
			}
			want := map[string]string{"session": tt.session, "host": tt.host, "url": tt.wantURL}
			for k, v := range want {
				if sent[0].Data[k] != v {
					t.Fatalf("data %v want %v", sent[0].Data, want)
				}
			}
			if len(sent[0].Data) != len(want) {
				t.Fatalf("data %v", sent[0].Data)
			}
		})
	}
}
