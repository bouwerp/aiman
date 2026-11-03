package agenthook

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeCodexRPC answers hooks/list with fixed entries and records every call.
type fakeCodexRPC struct {
	hooks   []codexHook
	listErr error
	calls   []string
	writes  []json.RawMessage
}

func (f *fakeCodexRPC) Call(_ context.Context, method string, params, result any) error {
	f.calls = append(f.calls, method)
	switch method {
	case codexMethodHooksList:
		if f.listErr != nil {
			return f.listErr
		}
		body, err := json.Marshal(map[string]any{"data": []any{map[string]any{"cwd": "/h", "hooks": f.hooks}}})
		if err != nil {
			return err
		}
		return json.Unmarshal(body, result)
	case codexMethodBatchWrite:
		body, err := json.Marshal(params)
		if err != nil {
			return err
		}
		f.writes = append(f.writes, body)
		return nil
	}
	return errors.New("unexpected method " + method)
}

func codexEntry(key, command, status string) codexHook {
	return codexHook{Key: key, Command: command, TrustStatus: status, CurrentHash: "sha256:" + key}
}

func TestTrustCodexHooksWritesOnlyAimanUntrustedOrModified(t *testing.T) {
	home := "/h"
	aiman := reporterPath(home)
	rpc := &fakeCodexRPC{hooks: []codexHook{
		codexEntry("graphify", "/h/.local/bin/graphify hook-check", "untrusted"),
		codexEntry("stop", aiman, "untrusted"),
		codexEntry("compact", aiman, "modified"),
		codexEntry("start", aiman, "trusted"),
		codexEntry("other-modified", "/usr/bin/other", "modified"),
		codexEntry("args", aiman+" --extra", "untrusted"),
	}}
	managed := codexEntry("managed", aiman, "untrusted")
	managed.IsManaged = true
	rpc.hooks = append(rpc.hooks, managed)

	keys, err := trustCodexHooks(context.Background(), rpc, home)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"stop", "compact"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys=%v want %v", keys, want)
	}
	if len(rpc.writes) != 1 {
		t.Fatalf("writes=%d", len(rpc.writes))
	}
	var got map[string]any
	if err := json.Unmarshal(rpc.writes[0], &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"edits": []any{map[string]any{
			"keyPath": "hooks.state",
			"value": map[string]any{
				"stop":    map[string]any{"trusted_hash": "sha256:stop"},
				"compact": map[string]any{"trusted_hash": "sha256:compact"},
			},
			"mergeStrategy": "upsert",
		}},
		"filePath":         nil,
		"expectedVersion":  nil,
		"reloadUserConfig": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("batchWrite params:\n got %v\nwant %v", got, want)
	}
}

func TestTrustCodexHooksNoWriteWhenNothingToTrust(t *testing.T) {
	home := "/h"
	rpc := &fakeCodexRPC{hooks: []codexHook{
		codexEntry("graphify", "/h/.local/bin/graphify hook-check", "untrusted"),
		codexEntry("stop", reporterPath(home), "trusted"),
	}}
	keys, err := trustCodexHooks(context.Background(), rpc, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 || len(rpc.writes) != 0 {
		t.Fatalf("keys=%v writes=%d", keys, len(rpc.writes))
	}
	if !reflect.DeepEqual(rpc.calls, []string{codexMethodHooksList}) {
		t.Fatalf("calls=%v", rpc.calls)
	}
}

func TestTrustCodexHooksListErrorWritesNothing(t *testing.T) {
	rpc := &fakeCodexRPC{listErr: errors.New("method not found")}
	keys, err := trustCodexHooks(context.Background(), rpc, "/h")
	if err == nil || !strings.Contains(err.Error(), "method not found") {
		t.Fatalf("err=%v", err)
	}
	if len(keys) != 0 || len(rpc.writes) != 0 {
		t.Fatalf("keys=%v writes=%d", keys, len(rpc.writes))
	}
}

func TestTrustCodexHooksSkipsHostWithoutCodexDir(t *testing.T) {
	keys, err := TrustCodexHooks(context.Background(), t.TempDir(), "")
	if err != nil || len(keys) != 0 {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
}

// fakeAppServer reads requests from r and answers each id with replies[method].
// It sends a notification first so the client must match by id.
func fakeAppServer(t *testing.T, r io.Reader, w io.WriteCloser, replies map[string]string) {
	t.Helper()
	defer w.Close()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		var req struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			t.Errorf("bad request %q: %v", sc.Text(), err)
			return
		}
		if req.ID == nil {
			continue
		}
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","method":"configWarning","params":{}}`+"\n")
		reply, err := json.Marshal(*req.ID)
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(reply)+`,`+replies[req.Method]+"}\n")
	}
}

func TestCodexConnMatchesResponsesByID(t *testing.T) {
	reqR, reqW := io.Pipe()
	respR, respW := io.Pipe()
	go fakeAppServer(t, reqR, respW, map[string]string{
		"initialize":          `"result":{}`,
		codexMethodHooksList:  `"result":{"data":[{"cwd":"/h","hooks":[{"key":"k","command":"c","trustStatus":"untrusted","currentHash":"sha256:x"}]}]}`,
		codexMethodBatchWrite: `"error":{"code":-32601,"message":"no such method"}`,
	})
	conn := newCodexConn(reqW, respR)
	if err := conn.handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	var list codexHooksListResult
	if err := conn.Call(context.Background(), codexMethodHooksList, map[string]any{"cwds": []string{"/h"}}, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data) != 1 || len(list.Data[0].Hooks) != 1 || list.Data[0].Hooks[0].CurrentHash != "sha256:x" {
		t.Fatalf("list=%+v", list)
	}
	err := conn.Call(context.Background(), codexMethodBatchWrite, map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "no such method") {
		t.Fatalf("err=%v", err)
	}
	_ = reqW.Close()
}

// serve's own PATH lacks ~/.local/bin, where codex installs itself, so the
// binary is looked up in the agent PATH serve passes in.
func TestCodexInFindsExecutablesOnlyInGivenPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, ok := codexIn("/nonexistent:" + dir); !ok || got != bin {
		t.Fatalf("codexIn = %q, %v; want %q", got, ok, bin)
	}
	if _, ok := codexIn("/nonexistent"); ok {
		t.Fatal("found codex outside the given path")
	}
	if err := os.Chmod(bin, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := codexIn(dir); ok {
		t.Fatal("a non-executable file is not codex")
	}
}
