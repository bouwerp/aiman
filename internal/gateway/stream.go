package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"

	"github.com/bouwerp/aiman/internal/server"
	"github.com/coder/websocket"
)

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	conn, err := acceptWS(w, r)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	stream, err := server.EventsDial(s.Socket)
	if err != nil {
		_ = conn.Close(websocket.StatusTryAgainLater, err.Error())
		return
	}
	defer stream.Close()

	ctx := conn.CloseRead(r.Context())
	for {
		ev, err := stream.Next()
		if err != nil {
			return
		}
		body, err := json.Marshal(ev)
		if err != nil {
			return
		}
		if err := conn.Write(ctx, websocket.MessageText, body); err != nil {
			return
		}
	}
}

func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	cols := queryInt(r, "cols", 80)
	rows := queryInt(r, "rows", 24)

	ws, err := acceptWS(w, r)
	if err != nil {
		return
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	att, err := server.AttachDial(s.Socket, id, cols, rows)
	if err != nil {
		_ = ws.Close(websocket.StatusTryAgainLater, err.Error())
		return
	}
	defer att.Close()

	bridgeAttach(r.Context(), ws, att)
}

type wsMsg struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

func bridgeAttach(ctx context.Context, ws *websocket.Conn, att *server.AttachConn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		pumpAttachOut(ctx, ws, att)
	}()
	go func() {
		defer wg.Done()
		pumpAttachIn(ctx, ws, att)
	}()
	wg.Wait()
}

func pumpAttachOut(ctx context.Context, ws *websocket.Conn, att *server.AttachConn) {
	buf := make([]byte, 32*1024)
	for {
		n, err := att.Read(buf)
		if n > 0 {
			if werr := ws.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func pumpAttachIn(ctx context.Context, ws *websocket.Conn, att *server.AttachConn) {
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			if err := att.WriteInput(data); err != nil {
				return
			}
			continue
		}
		var msg wsMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "input":
			if err := att.WriteInput([]byte(msg.Data)); err != nil {
				return
			}
		case "resize":
			if err := att.Resize(msg.Cols, msg.Rows); err != nil {
				return
			}
		}
	}
}

func acceptWS(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	// Origin is not a useful CSRF check here: React Native sends a variety of
	// origins (or none), and every route except health already requires a
	// bearer token.
	return websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
}

func queryInt(r *http.Request, name string, fallback int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
