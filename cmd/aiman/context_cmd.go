package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/contextstore"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
)

func printContextUsage(w io.Writer) {
	fmt.Fprint(w, `Usage: aiman context <ls|find|get|put|pack|import> …

  ls [--group G | --repo R] [--limit N]
  find QUERY [--group G | --repo R] [--limit N]
  get ID
  put --title T [--abstract A] [--group G | --repo R] [--body-file FILE]
  pack [--group G] [--repo R] [--limit N]
  import [--agent all|claude,grok,codex,agy] [--group G] [--repo R] [--dry-run]

Notes live in ~/.aiman/context/ on this host. Serve is preferred; if it
is down the commands read and write the files directly.

import copies agent memories from this user's home into the store
(Claude auto-memory, Grok ~/.grok/memory, Codex ~/.codex/memories,
agy walkthroughs). Re-running overwrites the same notes.
`)
}

func runContext(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printContextUsage(os.Stderr)
		return errUsage
	}
	if args[0] == "import" {
		return runContextImport(args[1:])
	}
	store, err := localContextStore()
	if err != nil {
		return err
	}
	sock, sockErr := socketPath()
	if sockErr == nil {
		err := contextViaServe(sock, args[0], args[1:])
		if err == nil || !errors.Is(err, server.ErrServerNotRunning) {
			return err
		}
	}
	return contextViaFiles(store, args[0], args[1:])
}

func runContextImport(args []string) error {
	flags, rest := takeFlags(args)
	raw := flags["agent"]
	if raw == "" {
		raw = strings.Join(rest, ",")
	}
	agents := contextstore.ParseImportAgents(raw)
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	files := contextstore.CollectMemories(home, agents)
	store, err := localContextStore()
	if err != nil {
		return err
	}
	dry := flags["dry-run"] != ""
	res, err := contextstore.ImportMemories(context.Background(), store, files, flags["group"], flags["repo"], dry)
	if err != nil {
		writeCLIError(server.CodeInvalidParams, err.Error())
		return err
	}
	if len(res.Agents) == 0 {
		res.Agents = agents
	}
	return writeJSON(res)
}

func localContextStore() (*contextstore.Files, error) {
	dir, err := config.GetDir()
	if err != nil {
		return nil, err
	}
	return contextstore.NewFiles(contextstore.Root(dir)), nil
}

func contextViaServe(sock, cmd string, args []string) error {
	method, params, err := contextRPC(cmd, args)
	if err != nil {
		return err
	}
	resp, err := server.Call(sock, method, params)
	if err != nil {
		if errors.Is(err, server.ErrServerNotRunning) {
			return err
		}
		writeCLIError(server.CodeInvalidParams, err.Error())
		return err
	}
	if resp.Error != nil {
		writeCLIError(resp.Error.Code, resp.Error.Message)
		return errors.New(resp.Error.Message)
	}
	return writeJSON(resp.Result)
}

func contextRPC(cmd string, args []string) (string, map[string]any, error) {
	flags, rest := takeFlags(args)
	params := contextParams(flags)
	switch cmd {
	case "ls", "list":
		return "context.list", params, nil
	case "find":
		params["text"] = strings.Join(rest, " ")
		return "context.find", params, nil
	case "get":
		if len(rest) < 1 {
			writeCLIError(server.CodeInvalidParams, "context get requires an id")
			return "", nil, errUsage
		}
		return "context.get", map[string]any{"id": rest[0]}, nil
	case "put":
		body, err := readPutBody(flags, rest)
		if err != nil {
			writeCLIError(server.CodeInvalidParams, err.Error())
			return "", nil, err
		}
		params["title"] = flags["title"]
		params["abstract"] = flags["abstract"]
		params["body"] = body
		if sid := strings.TrimSpace(os.Getenv("AIMAN_ID")); sid != "" {
			params["session"] = sid
		}
		if flags["title"] == "" {
			writeCLIError(server.CodeInvalidParams, "context put requires --title")
			return "", nil, errUsage
		}
		return "context.put", params, nil
	case "pack":
		if t := strings.Join(rest, " "); t != "" {
			params["text"] = t
		}
		return "context.pack", params, nil
	default:
		writeCLIError(server.CodeInvalidParams, "unknown context command "+cmd)
		return "", nil, errUsage
	}
}

func contextParams(flags map[string]string) map[string]any {
	params := map[string]any{}
	if g := flags["group"]; g != "" {
		params["group"] = g
	}
	if r := flags["repo"]; r != "" {
		params["repo"] = r
	}
	if ns := flags["ns"]; ns != "" {
		params["ns"] = ns
	}
	if k := flags["key"]; k != "" {
		params["key"] = k
	}
	if n := flags["limit"]; n != "" {
		params["limit"] = atoi(n)
	}
	return params
}

func readPutBody(flags map[string]string, rest []string) (string, error) {
	if p := flags["body-file"]; p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	if len(rest) > 0 {
		return strings.Join(rest, " "), nil
	}
	if stdinIsTTY() {
		return "", nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func contextViaFiles(store *contextstore.Files, cmd string, args []string) error {
	ctx := context.Background()
	flags, rest := takeFlags(args)
	q := filesQuery(flags, rest)
	switch cmd {
	case "ls", "list":
		list, err := store.List(ctx, q)
		if err != nil {
			return err
		}
		return writeJSON(map[string]any{"type": "context_list", "notes": notesJSON(list, false)})
	case "find":
		list, err := store.Find(ctx, q)
		if err != nil {
			return err
		}
		return writeJSON(map[string]any{"type": "context_find", "notes": notesJSON(list, false)})
	case "get":
		if len(rest) < 1 {
			writeCLIError(server.CodeInvalidParams, "context get requires an id")
			return errUsage
		}
		got, err := store.Get(ctx, rest[0])
		if err != nil {
			writeCLIError(server.CodeNotFound, err.Error())
			return err
		}
		return writeJSON(map[string]any{"type": "context_note", "note": noteJSON(*got, true)})
	case "put":
		body, err := readPutBody(flags, rest)
		if err != nil {
			writeCLIError(server.CodeInvalidParams, err.Error())
			return err
		}
		if flags["title"] == "" {
			writeCLIError(server.CodeInvalidParams, "context put requires --title")
			return errUsage
		}
		e := domain.ContextEntry{
			Title:     flags["title"],
			Abstract:  flags["abstract"],
			Body:      body,
			SessionID: strings.TrimSpace(os.Getenv("AIMAN_ID")),
		}
		e.Namespace, e.Key = contextNSKey(flags)
		stored, err := store.Put(ctx, e)
		if err != nil {
			writeCLIError(server.CodeInvalidParams, err.Error())
			return err
		}
		return writeJSON(map[string]any{"type": "context_put", "id": stored.ID, "note": noteJSON(stored, true)})
	case "pack":
		var text string
		if flags["group"] != "" || flags["repo"] != "" {
			text = contextstore.PackForSession(ctx, store, flags["group"], flags["repo"])
		} else {
			var err error
			text, err = store.Pack(ctx, q)
			if err != nil {
				return err
			}
		}
		return writeJSON(map[string]any{"type": "context_pack", "text": text})
	default:
		writeCLIError(server.CodeInvalidParams, "unknown context command "+cmd)
		return errUsage
	}
}

func filesQuery(flags map[string]string, rest []string) domain.ContextQuery {
	ns, key := contextNSKey(flags)
	q := domain.ContextQuery{Namespace: ns, Key: key, Limit: atoi(flags["limit"])}
	if len(rest) > 0 {
		q.Text = strings.Join(rest, " ")
	}
	return q
}

func contextNSKey(flags map[string]string) (ns, key string) {
	if g := flags["group"]; g != "" {
		return domain.ContextNSGroup, g
	}
	if r := flags["repo"]; r != "" {
		return domain.ContextNSRepo, r
	}
	return flags["ns"], flags["key"]
}

func notesJSON(list []domain.ContextEntry, body bool) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		out = append(out, noteJSON(e, body))
	}
	return out
}

func noteJSON(e domain.ContextEntry, body bool) map[string]any {
	n := map[string]any{
		"id":       e.ID,
		"ns":       e.Namespace,
		"key":      e.Key,
		"title":    e.Title,
		"abstract": e.Abstract,
	}
	if e.SessionID != "" {
		n["session"] = e.SessionID
	}
	if !e.CreatedAt.IsZero() {
		n["created"] = e.CreatedAt.UTC().Format(time.RFC3339)
	}
	if body {
		n["body"] = e.Body
	}
	return n
}
