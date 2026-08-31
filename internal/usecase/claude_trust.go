package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"
)

// workspaceTrustTimeout bounds pre-launch trust. Claude's ~/.claude.json write
// is milliseconds; the leftover Copilot CLI calls are the slow path.
const workspaceTrustTimeout = 15 * time.Second

// workspaceTrustRemote is the narrow remote surface workspace trust needs.
type workspaceTrustRemote interface {
	Execute(ctx context.Context, cmd string) (string, error)
	WriteFile(ctx context.Context, path string, content []byte) error
}

// mergeClaudeTrustedProjects sets hasTrustDialogAccepted=true for each path in
// a Claude Code ~/.claude.json document, preserving every other field.
//
// Claude Code no longer ships a `claude trust` subcommand. The only durable way
// to skip the first-run workspace-trust dialog is this projects map entry.
func mergeClaudeTrustedProjects(raw []byte, paths []string) ([]byte, error) {
	doc := map[string]any{}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
			return nil, fmt.Errorf("parse ~/.claude.json: %w", err)
		}
	}

	projects, _ := doc["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
		doc["projects"] = projects
	}

	seen := map[string]struct{}{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}

		entry, _ := projects[p].(map[string]any)
		if entry == nil {
			entry = map[string]any{}
		}
		entry["hasTrustDialogAccepted"] = true
		projects[p] = entry
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// EnsureClaudeWorkspaceTrusted marks each absolute workspace path as trusted in
// the remote user's ~/.claude.json so Claude Code skips its trust dialog on
// first launch in that directory.
func EnsureClaudeWorkspaceTrusted(ctx context.Context, remote workspaceTrustRemote, paths ...string) error {
	if remote == nil {
		return nil
	}
	cleaned := uniqueNonEmpty(paths)
	if len(cleaned) == 0 {
		return nil
	}

	home, err := remote.Execute(ctx, `printf %s "$HOME"`)
	if err != nil {
		return fmt.Errorf("resolve remote $HOME: %w", err)
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return fmt.Errorf("resolve remote $HOME: empty")
	}

	existing, _ := remote.Execute(ctx, `cat "$HOME/.claude.json" 2>/dev/null || true`)
	merged, err := mergeClaudeTrustedProjects([]byte(existing), cleaned)
	if err != nil {
		return err
	}
	// path.Join keeps forward slashes on the remote even when the laptop is macOS.
	dest := path.Join(home, ".claude.json")
	if err := remote.WriteFile(ctx, dest, merged); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}

func uniqueNonEmpty(paths []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// TrustWorkspacePaths marks a working directory (and optional worktree root) as
// trusted for the agents that gate on first-run folder approval. It must run
// before the agent process starts: trusting afterwards leaves the dialog up,
// and typing a prompt into it can select "No" and kill the agent.
func TrustWorkspacePaths(ctx context.Context, remote workspaceTrustRemote, workingDir, worktreePath string) {
	if remote == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, workspaceTrustTimeout)
	defer cancel()

	paths := uniqueNonEmpty([]string{workingDir, worktreePath})
	for _, p := range paths {
		_, _ = remote.Execute(ctx, fmt.Sprintf("git config --global --add safe.directory %q", p))
	}
	// Claude first: its trust dialog is what blocks a fresh PTY session, and the
	// fix is a JSON write rather than a CLI that no longer exists.
	_ = EnsureClaudeWorkspaceTrusted(ctx, remote, paths...)

	for _, p := range paths {
		_, _ = remote.Execute(ctx, fmt.Sprintf(
			"cd %q && if command -v copilot >/dev/null; then copilot trust . >/dev/null 2>&1 || copilot trust add . >/dev/null 2>&1; fi", p))
		_, _ = remote.Execute(ctx, fmt.Sprintf(
			"cd %q && if command -v gh >/dev/null; then gh copilot trust . >/dev/null 2>&1 || gh copilot trust add . >/dev/null 2>&1; fi", p))
	}
}

// trustWorkspaceBeforeLaunch is the FlowManager entry point used by CreateSession.
func (m *FlowManager) trustWorkspaceBeforeLaunch(ctx context.Context, remote workspaceTrustRemote, workingDir, worktreePath string) {
	TrustWorkspacePaths(ctx, remote, workingDir, worktreePath)
}
