package contextstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	maxImportBytes     = 64 << 10
	defaultImportLimit = 500
	abstractBudget     = 240
)

// MemoryFile is one agent memory file ready to become a context note.
type MemoryFile struct {
	Agent    string
	RelPath  string
	Title    string
	Abstract string
	Body     string
	Repo     string
	mtime    int64
}

// ImportedNote is one note written (or that would be written) by ImportMemories.
type ImportedNote struct {
	ID     string `json:"id"`
	Agent  string `json:"agent"`
	Title  string `json:"title"`
	NS     string `json:"ns"`
	Key    string `json:"key"`
	Path   string `json:"path"`
	DryRun bool   `json:"dry_run,omitempty"`
}

// ImportResult is the JSON payload for `aiman context import`.
type ImportResult struct {
	Type     string         `json:"type"`
	Imported int            `json:"imported"`
	Skipped  int            `json:"skipped"`
	DryRun   bool           `json:"dry_run,omitempty"`
	Agents   []string       `json:"agents"`
	Notes    []ImportedNote `json:"notes"`
}

// CollectMemories walks known agent memory dirs under home.
func CollectMemories(home string, agents []string) []MemoryFile {
	home = strings.TrimRight(home, "/\\")
	var out []MemoryFile
	for _, agent := range agents {
		switch agent {
		case "claude":
			out = append(out, collectClaude(home)...)
		case "grok":
			out = append(out, collectGrok(home)...)
		case "codex":
			out = append(out, collectCodex(home)...)
		case "agy":
			out = append(out, collectAgy(home)...)
		}
	}
	return out
}

// ImportMemories writes collected files into the store. Re-import of the same
// source overwrites via a stable id. group/repoOverride pin destination;
// otherwise a git origin slug or host/host is used.
func ImportMemories(ctx context.Context, store domain.ContextStore, files []MemoryFile, group, repoOverride string, dryRun bool) (ImportResult, error) {
	res := ImportResult{Type: "context_import", DryRun: dryRun, Notes: []ImportedNote{}}
	if store == nil && !dryRun {
		return res, fmt.Errorf("context store unavailable")
	}
	seenAgent := map[string]bool{}
	for _, f := range files {
		if strings.TrimSpace(f.Body) == "" && strings.TrimSpace(f.Abstract) == "" {
			res.Skipped++
			continue
		}
		e := memoryEntry(f, group, repoOverride)
		if !dryRun {
			stored, err := store.Put(ctx, e)
			if err != nil {
				res.Skipped++
				continue
			}
			e = stored
		}
		res.Imported++
		if !seenAgent[f.Agent] {
			seenAgent[f.Agent] = true
			res.Agents = append(res.Agents, f.Agent)
		}
		res.Notes = append(res.Notes, ImportedNote{
			ID:     e.ID,
			Agent:  f.Agent,
			Title:  e.Title,
			NS:     e.Namespace,
			Key:    e.Key,
			Path:   f.RelPath,
			DryRun: dryRun,
		})
	}
	return res, nil
}

func memoryEntry(f MemoryFile, group, repoOverride string) domain.ContextEntry {
	ns, key := importDest(f.Repo, group, repoOverride)
	title := strings.TrimSpace(f.Title)
	if title == "" {
		title = filepath.Base(f.RelPath)
	}
	label := agentLabel(f.Agent)
	if !strings.HasPrefix(strings.ToLower(title), strings.ToLower(label)) {
		title = label + ": " + title
	}
	abstract := strings.TrimSpace(f.Abstract)
	if abstract == "" {
		abstract = clipText(f.Body, abstractBudget)
	}
	body := strings.TrimSpace(f.Body)
	if body == "" {
		body = abstract
	}
	e := domain.ContextEntry{
		ID:        importID(f.Agent, f.RelPath),
		Namespace: ns,
		Key:       key,
		Title:     title,
		Abstract:  abstract,
		Body:      body,
	}
	if f.mtime > 0 {
		e.CreatedAt = unixUTC(f.mtime)
	}
	return e
}

func importDest(inferred, group, repoOverride string) (ns, key string) {
	if g := strings.TrimSpace(group); g != "" {
		return domain.ContextNSGroup, g
	}
	if r := strings.TrimSpace(repoOverride); r != "" {
		return domain.ContextNSRepo, r
	}
	if r := strings.TrimSpace(inferred); r != "" {
		return domain.ContextNSRepo, r
	}
	return domain.ContextNSHost, "host"
}

func importID(agent, rel string) string {
	sum := sha256.Sum256([]byte(agent + "\n" + rel))
	return "imp-" + hex.EncodeToString(sum[:8])
}

func ParseImportAgents(raw string) []string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" || raw == "all" {
		return []string{"claude", "grok", "codex", "agy"}
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		switch p {
		case "claude", "claude-code":
			p = "claude"
		case "grok", "grok-build":
			p = "grok"
		case "codex":
			p = "codex"
		case "agy", "antigravity", "gemini":
			p = "agy"
		case "all":
			return []string{"claude", "grok", "codex", "agy"}
		case "":
			continue
		default:
			continue
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	if len(out) == 0 {
		return []string{"claude", "grok", "codex", "agy"}
	}
	return out
}

func agentLabel(agent string) string {
	switch agent {
	case "claude":
		return "Claude"
	case "grok":
		return "Grok"
	case "codex":
		return "Codex"
	case "agy":
		return "agy"
	default:
		return agent
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func relFrom(home, abs string) string {
	rel, err := filepath.Rel(home, abs)
	if err != nil {
		return abs
	}
	return rel
}
