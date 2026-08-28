package gateway

import (
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/server"
)

func TestScanNotifiesFromSessionList(t *testing.T) {
	var mu sync.Mutex
	var sent []expoMessage
	sock := startEchoUnix(t, func(conn net.Conn, line []byte) {
		var req server.Request
		_ = json.Unmarshal(line, &req)
		out, _ := server.EncodeResponse(server.Response{
			ID: req.ID,
			Result: map[string]any{
				"type": "session_list",
				"sessions": []map[string]any{{
					"id":            "sess-1",
					"name":          "impl",
					"state":         "waiting_input",
					"state_message": "Continue?",
				}},
			},
		})
		_, _ = conn.Write(out)
	})
	n := newTestNotifier(t, func(msgs []expoMessage) error {
		mu.Lock()
		sent = append(sent, msgs...)
		mu.Unlock()
		return nil
	})
	if err := n.store.Register(PushDevice{Token: "ExponentPushToken[t]", DeviceID: "p"}); err != nil {
		t.Fatal(err)
	}
	n.scan(sock)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(sent)
		mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || sent[0].Data["url"] != "aimanphone://session/sess-1" {
		t.Fatalf("sent %+v", sent)
	}
}
