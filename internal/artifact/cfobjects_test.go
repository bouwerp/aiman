package artifact

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCFObjectsNormalETag(t *testing.T) {
	for in, want := range map[string]string{
		`W/"abc"`: `"abc"`,
		`"abc"`:   `"abc"`,
		`abc`:     `"abc"`,
		``:        ``,
	} {
		if got := normalETag(in); got != want {
			t.Errorf("normalETag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCFObjectsListPages(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		page := map[string]any{"success": true, "result": []any{map[string]any{"key": "a/1"}}, "result_info": map[string]any{"cursor": "c1", "is_truncated": true}}
		if r.URL.Query().Get("cursor") == "c1" {
			page = map[string]any{"success": true, "result": []any{map[string]any{"key": "a/2"}}, "result_info": nil}
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer srv.Close()
	b := NewCFObjects(srv.URL, "acc", "b", func(context.Context) (string, error) { return "t", nil }, srv.Client())
	keys, err := b.List(context.Background(), "a/")
	if err != nil || len(keys) != 2 || keys[1] != "a/2" || calls != 2 {
		t.Fatalf("keys %v err %v calls %d", keys, err, calls)
	}
}
