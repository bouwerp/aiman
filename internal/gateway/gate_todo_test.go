package gateway

import (
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
)

// todoSocket is a fake serve holding a gate (t1) and a plain item (t2). getErr
// makes todo.get fail, and getResult replaces its answer.
type todoSocket struct {
	mu        sync.Mutex
	methods   []string
	getErr    bool
	getResult any
}

func (f *todoSocket) start(t *testing.T) string {
	t.Helper()
	list := domain.TodoList{SessionID: "s1", Items: []domain.TodoItem{
		{ID: "t1", Text: "Design approved", State: domain.TodoPending, Ref: "gate:design"},
		{ID: "t2", Text: "write tests", State: domain.TodoPending, Ref: "check:lint"},
	}}
	return startEchoUnix(t, func(conn net.Conn, line []byte) {
		var req server.Request
		_ = json.Unmarshal(line, &req)
		f.mu.Lock()
		f.methods = append(f.methods, req.Method)
		f.mu.Unlock()
		resp := server.Response{ID: req.ID, Result: map[string]any{"type": "ok"}}
		if req.Method == "todo.get" {
			switch {
			case f.getErr:
				resp = server.Response{ID: req.ID, Error: &server.Error{Code: server.CodeNotFound, Message: "session not found"}}
			case f.getResult != nil:
				resp.Result = f.getResult
			default:
				resp.Result = map[string]any{"type": "todo_list", "list": list}
			}
		}
		out, _ := server.EncodeResponse(resp)
		_, _ = conn.Write(out)
	})
}

func (f *todoSocket) saw(method string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.methods, method)
}

func TestGateEditsOnPlainDevice(t *testing.T) {
	tests := []struct {
		name, body string
		wantCode   int
	}{
		{"close gate", `{"method":"todo.update","params":{"session":"s1","id":"t1","state":"done"}}`, http.StatusForbidden},
		{"drop gate", `{"method":"todo.update","params":{"session":"s1","id":"t1","state":"dropped"}}`, http.StatusForbidden},
		{"rename gate", `{"method":"todo.update","params":{"session":"s1","id":"t1","text":"ok"}}`, http.StatusForbidden},
		{"rename while reopening", `{"method":"todo.update","params":{"session":"s1","id":"t1","state":"pending","text":"ok"}}`, http.StatusForbidden},
		{"remove gate", `{"method":"todo.remove","params":{"session":"s1","id":"t1"}}`, http.StatusForbidden},
		{"reopen gate", `{"method":"todo.update","params":{"session":"s1","id":"t1","state":"pending"}}`, http.StatusOK},
		{"move gate", `{"method":"todo.move","params":{"session":"s1","id":"t1","to_index":0}}`, http.StatusOK},
		{"close plain item", `{"method":"todo.update","params":{"session":"s1","id":"t2","state":"done"}}`, http.StatusOK},
		{"remove plain item", `{"method":"todo.remove","params":{"session":"s1","id":"t2"}}`, http.StatusOK},
		{"unknown item goes to serve", `{"method":"todo.remove","params":{"session":"s1","id":"t9"}}`, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newDeviceRig(t, nil)
			fake := &todoSocket{}
			rig.srv.Socket = fake.start(t)
			_, tok := rig.device(t, false)
			rec := rig.do(http.MethodPost, "/v1/rpc", tok, tt.body)
			if rec.Code != tt.wantCode {
				t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
			}
			var body rpcBody
			if err := json.Unmarshal([]byte(tt.body), &body); err != nil {
				t.Fatal(err)
			}
			if fake.saw(body.Method) != (tt.wantCode == http.StatusOK) {
				t.Fatalf("forwarded=%v with code %d", fake.saw(body.Method), rec.Code)
			}
			if tt.wantCode == http.StatusForbidden && !strings.Contains(rec.Body.String(), "gate:design") {
				t.Fatalf("refusal does not name the gate: %s", rec.Body.String())
			}
		})
	}
}

// With no readable list the item may be a gate, so the edit stops.
func TestGateCheckFailsClosed(t *testing.T) {
	const edit = `{"method":"todo.update","params":{"session":"s1","id":"t1","state":"done"}}`
	tests := []struct {
		name     string
		fake     *todoSocket
		body     string
		wantCode int
	}{
		{"serve refuses the read", &todoSocket{getErr: true}, edit, http.StatusBadRequest},
		{"answer is not a list", &todoSocket{getResult: map[string]any{"type": "ok"}}, edit, http.StatusBadGateway},
		{"params are not an object", &todoSocket{}, `{"method":"todo.update","params":[1]}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newDeviceRig(t, nil)
			fake := tt.fake
			rig.srv.Socket = fake.start(t)
			_, tok := rig.device(t, false)
			rec := rig.do(http.MethodPost, "/v1/rpc", tok, tt.body)
			if rec.Code != tt.wantCode || fake.saw("todo.update") {
				t.Fatalf("code %d forwarded=%v body %s", rec.Code, fake.saw("todo.update"), rec.Body.String())
			}
		})
	}
	t.Run("socket down", func(t *testing.T) {
		rig := newDeviceRig(t, nil)
		rig.srv.Socket = "/nonexistent/aiman.sock"
		_, tok := rig.device(t, false)
		if rec := rig.do(http.MethodPost, "/v1/rpc", tok, edit); rec.Code != http.StatusBadGateway {
			t.Fatalf("code %d", rec.Code)
		}
	})
}

// The host token and a flagged device skip the read.
func TestGateCheckSkippedWithAuthority(t *testing.T) {
	const edit = `{"method":"todo.update","params":{"session":"s1","id":"t1","state":"done"}}`
	rig := newDeviceRig(t, nil)
	fake := &todoSocket{}
	rig.srv.Socket = fake.start(t)
	_, flagged := rig.device(t, true)
	for name, tok := range map[string]string{"host": hostToken, "device": flagged} {
		if rec := rig.do(http.MethodPost, "/v1/rpc", tok, edit); rec.Code != http.StatusOK {
			t.Fatalf("%s: code %d body %s", name, rec.Code, rec.Body.String())
		}
	}
	if fake.saw("todo.get") {
		t.Fatal("read the list for a caller that may change gates")
	}
}
