package console

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/edge"
)

// The editor port is a local DAP port for an editor (VS Code) on a debug run
// whose adapter opens child sessions (js-debug, debugpy). An editor cannot send the
// lease line, and VS Code starts a child session from the startDebugging
// configuration alone, which names no server, so the console stands between
// them:
//
//   - every connection opens with the console's editor lease token, so the
//     editor's child sessions join its lease at the edge; the editor's lease
//     is not the page's, so the two are separate holders;
//   - each startDebugging request from the adapter gains debugServer (this
//     port) and the editor's workspace folder, so VS Code connects the child
//     here too;
//   - source paths are mapped between the editor's workspace folder
//     (harness.LocalRootKey in its launch or attach request) and /src, the
//     tree js-debug reads in the adapter container. An editor that maps
//     paths itself (debugpy's pathMappings) sends no LocalRootKey, and no
//     path is rewritten.
type editorPort struct {
	ln   net.Listener
	port int
	// lease is the editor's lease token, apart from the page's.
	lease string
	// target is the dap forward's laptop port, 0 when there is none.
	target func() int

	wg sync.WaitGroup
}

// sourceRoot is where the run's tree is in the adapter container.
const sourceRoot = "/src"

func newEditorPort(target func() int) (*editorPort, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	lease, err := newToken()
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	e := &editorPort{ln: ln, port: ln.Addr().(*net.TCPAddr).Port, lease: lease, target: target}
	e.wg.Add(1)
	go e.accept()
	return e, nil
}

func (e *editorPort) close() {
	_ = e.ln.Close()
	e.wg.Wait()
}

func (e *editorPort) accept() {
	defer e.wg.Done()
	for {
		c, err := e.ln.Accept()
		if err != nil {
			return
		}
		go e.serve(c)
	}
}

func (e *editorPort) serve(client net.Conn) {
	defer func() { _ = client.Close() }()
	port := e.target()
	if port == 0 {
		return
	}
	up, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), dapDialTimeout)
	if err != nil {
		return
	}
	defer func() { _ = up.Close() }()
	if _, err := fmt.Fprintf(up, "%s%s\n", edge.LeasePrefix, e.lease); err != nil {
		return
	}
	m := &pathMap{}
	done := make(chan struct{}, 2)
	go func() {
		e.pump(client, up, m.toAdapter)
		done <- struct{}{}
	}()
	go func() {
		e.pump(up, client, m.toEditor(e.port))
		done <- struct{}{}
	}()
	<-done
	_ = client.Close()
	_ = up.Close()
	<-done
}

// pump reads DAP messages from src, passes each through edit and writes it
// to dst, until either side ends.
func (e *editorPort) pump(src, dst net.Conn, edit func(map[string]any)) {
	br := bufio.NewReader(src)
	for {
		body, err := edge.ReadDAPMessage(br, edge.MaxDAPMessage)
		if err != nil {
			return
		}
		var msg map[string]any
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if dec.Decode(&msg) == nil {
			edit(msg)
			if b, err := json.Marshal(msg); err == nil {
				body = b
			}
		}
		if err := edge.WriteDAPBody(dst, body); err != nil {
			return
		}
	}
}

// pathMap maps one connection's paths. The editor's root comes from its
// launch or attach request.
type pathMap struct {
	mu   sync.Mutex
	root string
}

func (m *pathMap) get() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.root
}

// toAdapter learns the editor's root and maps editor paths to /src.
func (m *pathMap) toAdapter(msg map[string]any) {
	if msg["type"] == "request" && (msg["command"] == "attach" || msg["command"] == "launch") {
		if args, ok := msg["arguments"].(map[string]any); ok {
			if root, ok := args[harness.LocalRootKey].(string); ok && strings.HasPrefix(root, "/") {
				m.mu.Lock()
				m.root = strings.TrimRight(root, "/")
				m.mu.Unlock()
			}
		}
	}
	if root := m.get(); root != "" {
		rewritePaths(msg, root, sourceRoot)
	}
}

// toEditor points each child session at this port and maps /src paths to
// the editor's root.
func (m *pathMap) toEditor(port int) func(map[string]any) {
	return func(msg map[string]any) {
		root := m.get()
		if msg["type"] == "request" && msg["command"] == "startDebugging" {
			if args, ok := msg["arguments"].(map[string]any); ok {
				if cfg, ok := args["configuration"].(map[string]any); ok {
					cfg["debugServer"] = port
					if root != "" {
						cfg[harness.LocalRootKey] = root
					}
				}
			}
		}
		if root != "" {
			rewritePaths(msg, sourceRoot, root)
		}
	}
}

// rewritePaths replaces the prefix from with to in every string under a
// "path" key, at any depth.
func rewritePaths(v any, from, to string) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && k == "path" {
				if s == from {
					x[k] = to
				} else if rest, ok := strings.CutPrefix(s, from+"/"); ok {
					x[k] = to + "/" + rest
				}
				continue
			}
			rewritePaths(val, from, to)
		}
	case []any:
		for _, val := range x {
			rewritePaths(val, from, to)
		}
	}
}
