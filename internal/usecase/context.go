package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/contextstore"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

const ContextPackPrompt = `Read ` + domain.AimanContextFileName + ` — it contains notes from earlier sessions on this host. Use aiman context get ID for the full note.`

type contextRemote interface {
	Execute(ctx context.Context, cmd string) (string, error)
	WriteFile(ctx context.Context, path string, content []byte) error
}

func FetchContextPack(ctx context.Context, remote contextRemote, group, repo string) string {
	if remote == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(`PATH="$HOME/.local/bin:$PATH" aiman context pack`)
	if g := strings.TrimSpace(group); g != "" {
		b.WriteString(" --group ")
		b.WriteString(posixQuote(g))
	}
	if r := strings.TrimSpace(repo); r != "" {
		b.WriteString(" --repo ")
		b.WriteString(posixQuote(r))
	}
	out, err := remote.Execute(ctx, b.String())
	if err != nil {
		return ""
	}
	return parseContextPackOutput(out)
}

func parseContextPackOutput(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(out), &parsed) == nil && strings.TrimSpace(parsed.Text) != "" {
		return parsed.Text
	}
	if i := strings.Index(out, "{"); i >= 0 {
		if json.Unmarshal([]byte(out[i:]), &parsed) == nil && strings.TrimSpace(parsed.Text) != "" {
			return parsed.Text
		}
	}
	if strings.Contains(out, "# Shared context") {
		return out
	}
	return ""
}

func InjectSharedContext(ctx context.Context, remote contextRemote, worktreePath, group, repo, prompt string) string {
	if remote == nil {
		return prompt
	}
	pack := FetchContextPack(ctx, remote, group, repo)
	if strings.TrimSpace(pack) == "" {
		return prompt
	}
	if err := contextstore.WriteSessionPack(ctx, remote, worktreePath, pack); err != nil {
		return prompt
	}
	// Context first, task second. Agents act on the first instruction they read
	// and treat what follows as detail, so a task ahead of the pointer to earlier
	// notes gets started before the notes are opened — which is the whole point
	// of writing them.
	return joinPrompt(ContextPackPrompt, prompt)
}

func PutSnapshotContext(ctx context.Context, remote contextRemote, snap *domain.SessionSnapshot, group string) error {
	if remote == nil || snap == nil {
		return nil
	}
	e := contextstore.EntryFromSnapshot(snap, group, snap.SessionID)
	homeOut, err := remote.Execute(ctx, `printf %s "$HOME"`)
	if err != nil {
		return fmt.Errorf("remote home: %w", err)
	}
	home := strings.TrimSpace(homeOut)
	if home == "" {
		return fmt.Errorf("remote home is empty")
	}
	root := filepath.Join(home, config.DirName, contextstore.DirName)
	p, body, err := contextstore.EncodeFile(root, e)
	if err != nil {
		return err
	}
	if _, err := remote.Execute(ctx, "mkdir -p "+posixQuote(filepath.Dir(p))); err != nil {
		return fmt.Errorf("creating context dir: %w", err)
	}
	return remote.WriteFile(ctx, p, body)
}

func posixQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
