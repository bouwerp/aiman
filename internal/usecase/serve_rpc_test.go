package usecase

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"testing"
)

type rpcRecorder struct {
	cmd string
	out string
	err error
}

func (r *rpcRecorder) Execute(_ context.Context, cmd string) (string, error) {
	r.cmd = cmd
	return r.out, r.err
}

func (r *rpcRecorder) WriteFile(context.Context, string, []byte) error { return nil }

func TestServeRPCNeverShellParsesText(t *testing.T) {
	text := "it's \"$(rm -rf ~)\" `id`\nnext ✓"
	rec := &rpcRecorder{out: `{"result":{"ok":true}}`}
	if _, err := ServeRPC(context.Background(), rec, "todo.add", map[string]any{"text": text}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.cmd, "$(") || strings.Contains(rec.cmd, "`") || strings.Contains(rec.cmd, "\n") {
		t.Fatalf("command exposes text to the shell: %q", rec.cmd)
	}
	m := regexp.MustCompile(`printf %s '([^']*)'`).FindStringSubmatch(rec.cmd)
	if m == nil {
		t.Fatalf("no base64 segment in %q", rec.cmd)
	}
	dec, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dec), `\"$(rm -rf ~)\"`) || !strings.Contains(string(dec), "next ✓") {
		t.Fatalf("decoded params lost the text: %s", dec)
	}
	if !strings.Contains(rec.cmd, "aiman rpc todo.add --params-stdin") {
		t.Fatalf("unexpected command: %q", rec.cmd)
	}
}

func TestServeRPCErrors(t *testing.T) {
	t.Run("unsupported", func(t *testing.T) {
		rec := &rpcRecorder{out: "aiman: unknown method todo.summary", err: errors.New("exit 1")}
		_, err := ServeRPC(context.Background(), rec, "todo.summary", map[string]any{})
		if !errors.Is(err, ErrRPCUnsupported) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("old binary", func(t *testing.T) {
		rec := &rpcRecorder{out: `aiman: unknown command "rpc"`, err: errors.New("exit 1")}
		_, err := ServeRPC(context.Background(), rec, "todo.get", map[string]any{})
		if !errors.Is(err, ErrRPCUnsupported) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("not running", func(t *testing.T) {
		rec := &rpcRecorder{out: `{"error":{"code":"server_not_running"}}`, err: errors.New("exit 1")}
		_, err := ServeRPC(context.Background(), rec, "todo.get", map[string]any{})
		if !errors.Is(err, ErrServeNotRunning) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("rpc error", func(t *testing.T) {
		rec := &rpcRecorder{out: `{"error":{"code":"not_found","message":"x"}}`, err: errors.New("exit 1")}
		_, err := ServeRPC(context.Background(), rec, "todo.get", map[string]any{})
		var re *RPCError
		if !errors.As(err, &re) || re.Code != "not_found" || re.Message != "x" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("result after noise", func(t *testing.T) {
		rec := &rpcRecorder{out: "noise\n" + `{"result":{"type":"todo_list"}}` + "\n"}
		got, err := ServeRPC(context.Background(), rec, "todo.get", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != `{"type":"todo_list"}` {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("transport error without answer", func(t *testing.T) {
		rec := &rpcRecorder{out: "", err: errors.New("ssh down")}
		_, err := ServeRPC(context.Background(), rec, "todo.get", map[string]any{})
		if err == nil || errors.Is(err, ErrRPCUnsupported) {
			t.Fatalf("err = %v", err)
		}
	})
}
