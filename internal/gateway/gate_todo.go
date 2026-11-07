package gateway

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
)

// gateRefPrefix marks a to-do that stands for an operator's sign-off.
const gateRefPrefix = "gate:"

// todoEdit holds the todo.update and todo.remove params the gate check reads.
// It decodes the same way serve does, so both see the same item.
type todoEdit struct {
	Session string  `json:"session"`
	ID      string  `json:"id"`
	Text    *string `json:"text"`
	State   string  `json:"state"`
}

// gateCheckedMethod reports whether method can close or drop an item. todo.move
// is left out because it only reorders.
func gateCheckedMethod(method string) bool {
	return method == "todo.update" || method == "todo.remove"
}

// gateEditAllowedPlain reports whether a device without allow_destructive may
// make this edit to a gate item. Only a move to an open state qualifies: that
// cannot stand in for the operator's sign-off.
func gateEditAllowedPlain(method string, e todoEdit) bool {
	if method != "todo.update" || e.Text != nil {
		return false
	}
	state := domain.TodoState(e.State)
	return state == domain.TodoPending || state == domain.TodoActive
}

// admitTodoEdit lets a device without allow_destructive through unless the edit
// would close, drop, rename or remove a gate. A list it cannot read refuses the
// edit, since it may hold a gate. An id the list does not hold goes on to serve,
// which answers not_found.
func (s *Server) admitTodoEdit(w http.ResponseWriter, body rpcBody) bool {
	var edit todoEdit
	if len(body.Params) > 0 {
		if err := json.Unmarshal(body.Params, &edit); err != nil {
			writeJSONError(w, http.StatusBadRequest, body.ID, server.CodeInvalidParams, err.Error())
			return false
		}
	}
	if gateEditAllowedPlain(body.Method, edit) {
		return true
	}
	list, ok := s.readTodoList(w, body.ID, edit.Session)
	if !ok {
		return false
	}
	for _, it := range list.Items {
		if it.ID == edit.ID && strings.HasPrefix(it.Ref, gateRefPrefix) {
			writeJSONError(w, http.StatusForbidden, body.ID, "forbidden", "changing gate "+it.Ref+" needs allow_destructive")
			return false
		}
	}
	return true
}

// readTodoList fetches a session's list through serve, answering the request
// when it cannot.
func (s *Server) readTodoList(w http.ResponseWriter, id, session string) (domain.TodoList, bool) {
	params, err := json.Marshal(map[string]string{"session": session})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return domain.TodoList{}, false
	}
	resp, err := server.CallRaw(s.Socket, "todo.get", params)
	if err != nil || resp.Error != nil {
		resp.ID = id
		writeSocketResponse(w, resp, err)
		return domain.TodoList{}, false
	}
	var result struct {
		Type string          `json:"type"`
		List domain.TodoList `json:"list"`
	}
	raw, err := json.Marshal(resp.Result)
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	if err != nil || result.Type != "todo_list" {
		http.Error(w, "unreadable to-do list", http.StatusBadGateway)
		return domain.TodoList{}, false
	}
	return result.List, true
}
