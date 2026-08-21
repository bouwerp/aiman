package server

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"id":"req_1","method":"session.list","params":{"group":"quick"},"extra":"ignored"}`)
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.ID != "req_1" || req.Method != "session.list" {
		t.Fatalf("got id=%q method=%q", req.ID, req.Method)
	}
	var params map[string]string
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("params: %v", err)
	}
	if params["group"] != "quick" {
		t.Fatalf("params = %#v", params)
	}
}

func TestEncodeSuccessAndError(t *testing.T) {
	t.Parallel()
	ok, err := EncodeResponse(Response{ID: "1", Result: map[string]any{"type": "pong", "protocol": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(ok, []byte(`"type":"pong"`)) {
		t.Fatalf("success = %s", ok)
	}
	fail, err := EncodeResponse(Response{ID: "1", Error: &Error{Code: CodeNotFound, Message: "nope"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(fail, []byte(`"code":"not_found"`)) {
		t.Fatalf("error = %s", fail)
	}
}
