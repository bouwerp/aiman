package gateway

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/notify"
	"github.com/bouwerp/aiman/internal/server"
)

func startEchoUnixNotify(t *testing.T, fn func(map[string]any) (map[string]any, *server.Error)) string {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 4096)
				n, _ := conn.Read(buf)
				var req server.Request
				_ = json.Unmarshal(buf[:n], &req)
				var p map[string]any
				if len(req.Params) > 0 {
					_ = json.Unmarshal(req.Params, &p)
				}
				res, errResp := fn(p)
				if errResp != nil {
					b, _ := json.Marshal(server.Response{ID: req.ID, Error: errResp})
					conn.Write(b)
					conn.Write([]byte("\n"))
					return
				}
				b, _ := json.Marshal(server.Response{ID: req.ID, Result: res})
				conn.Write(b)
				conn.Write([]byte("\n"))
			}(c)
		}
	}()
	t.Cleanup(func() { ln.Close(); os.Remove(sock) })
	return sock
}

func TestSocketSourceReadsNotifySince(t *testing.T) {
	sock := startEchoUnixNotify(t, func(p map[string]any) (map[string]any, *server.Error) {
		var after float64
		if v, ok := p["after"].(float64); ok {
			after = v
		}
		if int64(after) != 4 {
			t.Fatalf("after=%T%v", p["after"], p["after"])
		}
		return map[string]any{
			"type": "notifications",
			"entries": []map[string]any{
				{"seq": int64(5), "session_id": "s1", "kind": "waiting_input", "label": "P-1", "host": "h", "text": "hi", "at": time.Now().UTC().Format(time.RFC3339)},
			},
			"last_seq": int64(5),
		}, nil
	})
	src := SocketSource{Socket: sock}
	entries, last, err := src.Since(context.Background(), 4, 10)
	if err != nil {
		t.Fatal(err)
	}
	if last != 5 || len(entries) != 1 || entries[0].Seq != 5 || entries[0].Kind != domain.NotifyWaitingInput || entries[0].Text != "hi" {
		t.Fatalf("%+v last=%d", entries, last)
	}
}

func TestPhoneSinkMapsKindsToDeviceStates(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenPushStore(filepath.Join(dir, "p.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Register(PushDevice{Token: "t1", DeviceID: "d1", States: []string{"waiting_input"}}); err != nil {
		t.Fatal(err)
	}
	var sent []expoMessage
	sink := NewPhoneSink(store, func(m []expoMessage) error { sent = append(sent, m...); return nil })
	if err := sink.Send(context.Background(), domain.Notification{SessionID: "s1", Kind: domain.NotifyReviewDone, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Fatalf("sent %d", len(sent))
	}
	sent = nil
	if err := sink.Send(context.Background(), domain.Notification{SessionID: "s1", Kind: domain.NotifyEnded, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 0 {
		t.Fatalf("ended should not send to waiting_input device: %+v", sent)
	}
	sent = nil
	store2, _ := OpenPushStore(filepath.Join(dir, "p2.json"))
	_ = store2.Register(PushDevice{Token: "t2", DeviceID: "d2", States: []string{"exited"}})
	sink2 := NewPhoneSink(store2, func(m []expoMessage) error { sent = append(sent, m...); return nil })
	if err := sink2.Send(context.Background(), domain.Notification{SessionID: "s1", Kind: domain.NotifyEnded, Text: "bye"}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Fatalf("sent %d", len(sent))
	}
	if sent[0].Data["url"] != "aimanphone://session/s1" {
		t.Fatalf("url %v", sent[0].Data)
	}
}

func TestGatewayDoesNotDoublePush(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenPushStore(filepath.Join(dir, "p.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Register(PushDevice{Token: "t1", DeviceID: "d1", States: []string{"waiting_input"}}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	sent := 0
	src := &memSrc{entries: []domain.Notification{{Seq: 1, SessionID: "s1", Kind: domain.NotifyWaitingInput, Text: "hi"}, {Seq: 2, SessionID: "s1", Kind: domain.NotifyWaitingInput, Text: "hi2"}}}
	curPath := filepath.Join(dir, "c")
	os.WriteFile(curPath, []byte("0"), 0o600)
	cur := notify.FileCursor{Path: curPath}
	d := notify.NewDispatcher(src, cur, NewPhoneSink(store, func([]expoMessage) error { mu.Lock(); sent++; mu.Unlock(); return nil }))
	ctx := context.Background()
	if err := d.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if sent != 2 {
		t.Fatalf("sent %d", sent)
	}
	d2 := notify.NewDispatcher(src, cur, NewPhoneSink(store, func([]expoMessage) error { mu.Lock(); sent++; mu.Unlock(); return nil }))
	if err := d2.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if sent != 2 {
		t.Fatalf("double push sent %d", sent)
	}
}

type memSrc struct {
	entries []domain.Notification
	last    int64
	mu      sync.Mutex
}

func (m *memSrc) Since(ctx context.Context, after int64, limit int) ([]domain.Notification, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := []domain.Notification{}
	for _, e := range m.entries {
		if e.Seq > after {
			res = append(res, e)
		}
	}
	if len(res) > limit && limit > 0 {
		res = res[:limit]
	}
	if len(m.entries) > 0 {
		m.last = m.entries[len(m.entries)-1].Seq
	} else {
		m.last = after
	}
	return res, m.last, nil
}
