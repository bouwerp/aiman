//go:build feedaccept || feedaccept_p11

package gateway

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
)

// Acceptance tests for event-feed piece P11 (docs/specs/event-feed.md §11).

// feedSocket is a fake serve holding action 12: kind slack_draft, whose
// options are 0 slack_draft (inherited), 1 todo_add, 2 slack_reply, 3 read.
// getErr makes feed.actions fail, and getResult replaces its answer.
type feedSocket struct {
	mu        sync.Mutex
	methods   []string
	getErr    bool
	getResult any
}

func feedGateAction() domain.FeedAction {
	return domain.FeedAction{
		ID: 12, BatchID: 37, Kind: domain.FeedSlackDraft, Priority: "high", State: "proposed",
		Answers: []int64{811},
		Options: []domain.FeedOption{
			{Label: "Not yet", Params: json.RawMessage(`{"channel":"C1","text":"Not yet."}`)},
			{Label: "Remind me", Kind: domain.FeedTodoAdd, Params: json.RawMessage(`{"session":"s1","text":"answer Ana"}`)},
			{Label: "Reply now", Kind: domain.FeedSlackReply, Params: json.RawMessage(`{"channel":"C1","text":"No."}`)},
			{Label: "Read the PR", Kind: domain.FeedRead, Params: json.RawMessage(`{"url":"https://github.com/o/r/pull/1","why":"context"}`)},
		},
	}
}

func (f *feedSocket) start(t *testing.T) string {
	t.Helper()
	return startEchoUnix(t, func(conn net.Conn, line []byte) {
		var req server.Request
		_ = json.Unmarshal(line, &req)
		f.mu.Lock()
		f.methods = append(f.methods, req.Method)
		f.mu.Unlock()
		resp := server.Response{ID: req.ID, Result: map[string]any{"type": "feed_action"}}
		if req.Method == "feed.actions" {
			switch {
			case f.getErr:
				resp = server.Response{ID: req.ID, Error: &server.Error{Code: server.CodeNotFound, Message: "no such action"}}
			case f.getResult != nil:
				resp.Result = f.getResult
			default:
				resp.Result = map[string]any{"type": "feed_actions", "actions": []domain.FeedAction{feedGateAction()}}
			}
		}
		out, _ := server.EncodeResponse(resp)
		_, _ = conn.Write(out)
	})
}

func (f *feedSocket) saw(method string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.methods, method)
}

func TestAllowlistFeedMethods(t *testing.T) {
	for _, m := range []string{"feed.status", "feed.actions", "feed.events", "feed.memory"} {
		if !slices.Contains(deviceReadMethods, m) {
			t.Errorf("%s is not a device read method", m)
		}
	}
	if !slices.Contains(deviceWriteMethods, "feed.act") {
		t.Error("feed.act is not on deviceWriteMethods")
	}
	if slices.Contains(deviceDestructiveMethods, "feed.act") {
		t.Error("feed.act must not need allow_destructive as a whole: dismiss passes for any device")
	}
}

func TestFeedActGate(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		destructive bool
		refused     bool
	}{
		{"dismiss", `{"method":"feed.act","params":{"id":12,"op":"dismiss","reason":"not_relevant"}}`, false, false},
		{"approve inherited slack_draft", `{"method":"feed.act","params":{"id":12,"op":"approve","option":0}}`, false, false},
		{"approve todo_add", `{"method":"feed.act","params":{"id":12,"op":"approve","option":1}}`, false, false},
		{"approve read", `{"method":"feed.act","params":{"id":12,"op":"approve","option":3}}`, false, false},
		{"approve slack_reply on a slack_draft action", `{"method":"feed.act","params":{"id":12,"op":"approve","option":2}}`, false, true},
		{"edited slack_reply", `{"method":"feed.act","params":{"id":12,"op":"approve","option":2,"params":{"channel":"C1","text":"x"}}}`, false, true},
		{"approve slack_reply with flag", `{"method":"feed.act","params":{"id":12,"op":"approve","option":2}}`, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newDeviceRig(t, nil)
			fake := &feedSocket{}
			rig.srv.Socket = fake.start(t)
			_, tok := rig.device(t, tt.destructive)
			rec := rig.do(http.MethodPost, "/v1/rpc", tok, tt.body)
			if !tt.refused {
				if rec.Code != http.StatusOK || !fake.saw("feed.act") {
					t.Fatalf("code %d forwarded=%v body %s", rec.Code, fake.saw("feed.act"), rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusForbidden || fake.saw("feed.act") {
				t.Fatalf("code %d forwarded=%v body %s, want 403 and not forwarded", rec.Code, fake.saw("feed.act"), rec.Body.String())
			}
			var resp struct {
				Error struct {
					Code string `json:"code"`
					Data struct {
						Method string `json:"method"`
						Needs  string `json:"needs"`
					} `json:"data"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("body %s: %v", rec.Body.String(), err)
			}
			if resp.Error.Code != "forbidden" || resp.Error.Data.Method != "feed.act" || resp.Error.Data.Needs != "allow_destructive" {
				t.Fatalf("refusal %+v, want forbidden with data {feed.act, allow_destructive}", resp.Error)
			}
		})
	}
}

// dismiss needs no read: any device may dismiss.
func TestFeedActDismissSkipsTheRead(t *testing.T) {
	rig := newDeviceRig(t, nil)
	fake := &feedSocket{getErr: true}
	rig.srv.Socket = fake.start(t)
	_, tok := rig.device(t, false)
	rec := rig.do(http.MethodPost, "/v1/rpc", tok, `{"method":"feed.act","params":{"id":12,"op":"dismiss","reason":"handled"}}`)
	if rec.Code != http.StatusOK || !fake.saw("feed.act") || fake.saw("feed.actions") {
		t.Fatalf("code %d act=%v read=%v body %s", rec.Code, fake.saw("feed.act"), fake.saw("feed.actions"), rec.Body.String())
	}
}

// With no readable action the option's kind is unknown, so the approve stops.
func TestFeedActGateFailsClosed(t *testing.T) {
	const approve = `{"method":"feed.act","params":{"id":12,"op":"approve","option":0}}`
	tests := []struct {
		name string
		fake *feedSocket
		body string
	}{
		{"serve refuses the read", &feedSocket{getErr: true}, approve},
		{"answer is not an action list", &feedSocket{getResult: map[string]any{"type": "ok"}}, approve},
		{"params are not an object", &feedSocket{}, `{"method":"feed.act","params":[1]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newDeviceRig(t, nil)
			rig.srv.Socket = tt.fake.start(t)
			_, tok := rig.device(t, false)
			rec := rig.do(http.MethodPost, "/v1/rpc", tok, tt.body)
			if rec.Code == http.StatusOK || tt.fake.saw("feed.act") {
				t.Fatalf("code %d forwarded=%v body %s", rec.Code, tt.fake.saw("feed.act"), rec.Body.String())
			}
		})
	}
}

func TestPushKindsFeedAction(t *testing.T) {
	kind := domain.NotifyKind("feed_action")
	if !pushKinds[kind] {
		t.Fatal("pushKinds lacks feed_action")
	}
	store, err := OpenPushStore(filepath.Join(t.TempDir(), PushFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Register(PushDevice{Token: "ExponentPushToken[x]", DeviceID: "d1", States: []string{stateWaitingInput}}); err != nil {
		t.Fatal(err)
	}
	var sent []expoMessage
	sink := NewPhoneSink(store, "regent0", func(_ context.Context, msgs []expoMessage) error {
		sent = append(sent, msgs...)
		return nil
	})
	if err := sink.Send(context.Background(), domain.Notification{Kind: kind, Text: "Ana asks about the settler change"}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Fatalf("sent %d pushes, want 1", len(sent))
	}
	d := sent[0].Data
	if d["session"] != "" || !strings.HasSuffix(d["url"], "/feed") {
		t.Fatalf("push data %v, want the Feed tab and no session", d)
	}
}
