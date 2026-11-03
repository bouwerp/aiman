package agenthook

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// CodexTrustTimeout bounds one codex app-server exchange, process start included.
const CodexTrustTimeout = 20 * time.Second

const (
	codexMethodHooksList  = "hooks/list"
	codexMethodBatchWrite = "config/batchWrite"
)

// codexHook is one entry of codex's hooks/list. Codex computes CurrentHash over
// its own normalized identity of the hook, so aiman trusts by echoing it back
// rather than hashing.
type codexHook struct {
	Key         string `json:"key"`
	Command     string `json:"command"`
	TrustStatus string `json:"trustStatus"`
	CurrentHash string `json:"currentHash"`
	IsManaged   bool   `json:"isManaged"`
}

type codexHooksListResult struct {
	Data []struct {
		Hooks []codexHook `json:"hooks"`
	} `json:"data"`
}

// codexRPC is one JSON-RPC request to codex app-server; result may be nil.
type codexRPC interface {
	Call(ctx context.Context, method string, params, result any) error
}

// TrustCodexHooks marks aiman's own Codex hooks trusted so Codex does not ask
// for a review at every launch. It asks `codex app-server` for the hook list
// and writes `hooks.state.<key>.trusted_hash` (the same edit Codex's own
// review dialog makes) only for entries whose command is an aiman hook script
// and whose status is untrusted or modified. Every other hook is left for the
// user to review. codex is looked up in path, which the app-server also
// runs with: serve's own PATH lacks the user install dirs such as
// ~/.local/bin. A host without ~/.codex or a codex binary is skipped. It
// returns the keys it trusted.
func TrustCodexHooks(ctx context.Context, home, path string) ([]string, error) {
	if !isDir(filepath.Join(home, ".codex")) {
		return nil, nil
	}
	bin, ok := codexIn(path)
	if !ok {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, CodexTrustTimeout)
	defer cancel()
	conn, stop, err := startCodexAppServer(ctx, bin, home, path)
	if err != nil {
		return nil, err
	}
	defer stop()
	if err := conn.handshake(ctx); err != nil {
		return nil, err
	}
	return trustCodexHooks(ctx, conn, home)
}

func trustCodexHooks(ctx context.Context, rpc codexRPC, home string) ([]string, error) {
	var list codexHooksListResult
	if err := rpc.Call(ctx, codexMethodHooksList, map[string]any{"cwds": []string{home}}, &list); err != nil {
		return nil, fmt.Errorf("codex hooks/list: %w", err)
	}
	own := map[string]bool{reporterPath(home): true, todoReporterPath(home): true, gatePath(home): true}
	state := map[string]any{}
	var keys []string
	for _, d := range list.Data {
		for _, h := range d.Hooks {
			if !own[h.Command] || h.IsManaged || h.Key == "" || h.CurrentHash == "" {
				continue
			}
			if h.TrustStatus != "untrusted" && h.TrustStatus != "modified" {
				continue
			}
			if _, dup := state[h.Key]; dup {
				continue
			}
			state[h.Key] = map[string]string{"trusted_hash": h.CurrentHash}
			keys = append(keys, h.Key)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	params := map[string]any{
		"edits": []any{map[string]any{
			"keyPath":       "hooks.state",
			"value":         state,
			"mergeStrategy": "upsert",
		}},
		"filePath":         nil,
		"expectedVersion":  nil,
		"reloadUserConfig": true,
	}
	if err := rpc.Call(ctx, codexMethodBatchWrite, params, nil); err != nil {
		return nil, fmt.Errorf("codex config/batchWrite: %w", err)
	}
	return keys, nil
}

// startCodexAppServer runs `codex app-server` on pipes (no TTY) with HOME set
// to home and PATH to path. The process is killed when ctx ends or stop is
// called.
func startCodexAppServer(ctx context.Context, bin, home, path string) (*codexConn, func(), error) {
	cmd := exec.CommandContext(ctx, bin, "app-server")
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+path)
	cmd.Dir = home
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start codex app-server: %w", err)
	}
	stop := func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	return newCodexConn(stdin, stdout), stop, nil
}

// codexIn finds an executable codex in the PATH-style list path.
func codexIn(path string) (string, bool) {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, "codex")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}

// codexConn is a line-delimited JSON-RPC 2.0 client. Responses are matched
// by id; notifications and other ids in between are skipped. A blocked read
// ends when the process is killed and its stdout closes.
type codexConn struct {
	w    io.Writer
	r    *bufio.Reader
	next int
}

func newCodexConn(w io.Writer, r io.Reader) *codexConn {
	return &codexConn{w: w, r: bufio.NewReaderSize(r, 64*1024)}
}

func (c *codexConn) handshake(ctx context.Context) error {
	init := map[string]any{"clientInfo": map[string]string{"name": "aiman", "version": "0.0.0"}}
	if err := c.Call(ctx, "initialize", init, nil); err != nil {
		return fmt.Errorf("codex initialize: %w", err)
	}
	return c.send(map[string]any{"jsonrpc": "2.0", "method": "initialized"})
}

func (c *codexConn) send(msg any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.w.Write(append(body, '\n'))
	return err
}

func (c *codexConn) Call(ctx context.Context, method string, params, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.next++
	id := c.next
	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		line, err := c.r.ReadBytes('\n')
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("read %s response: %w", method, err)
		}
		var resp struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(line, &resp) != nil || resp.ID == nil || *resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return fmt.Errorf("%s: %s (code %d)", method, resp.Error.Message, resp.Error.Code)
		}
		if result == nil {
			return nil
		}
		if len(resp.Result) == 0 {
			return errors.New(method + ": empty result")
		}
		return json.Unmarshal(resp.Result, result)
	}
}
