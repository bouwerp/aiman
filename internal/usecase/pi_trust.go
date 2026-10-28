package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// piTrustProbe prints $HOME, then "pi" and the trust store when Pi is set up
// on the host. One round trip covers both.
const piTrustProbe = `printf '%s\n' "$HOME"; if [ -d "$HOME/.pi/agent" ]; then printf 'pi\n'; cat "$HOME/.pi/agent/trust.json" 2>/dev/null; fi`

// EnsurePiWorkspaceTrusted records each path as trusted in the remote
// ~/.pi/agent/trust.json, so Pi does not stop on "Trust project folder?" when
// the worktree carries project resources (aiman's own .agents/skills copy is
// one). A host without ~/.pi/agent is left alone, a path the user marked
// "Do not trust" keeps that decision, and a store Pi could not parse is not
// rewritten.
func EnsurePiWorkspaceTrusted(ctx context.Context, remote workspaceTrustRemote, paths ...string) error {
	if remote == nil {
		return nil
	}
	cleaned := uniqueNonEmpty(paths)
	if len(cleaned) == 0 {
		return nil
	}
	out, err := remote.Execute(ctx, piTrustProbe)
	if err != nil {
		return fmt.Errorf("probe pi trust store: %w", err)
	}
	home, rest, _ := strings.Cut(out, "\n")
	home = strings.TrimSpace(home)
	marker, raw, _ := strings.Cut(rest, "\n")
	if home == "" || strings.TrimSpace(marker) != "pi" {
		return nil
	}
	store := map[string]any{}
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &store); err != nil {
			return fmt.Errorf("parse ~/.pi/agent/trust.json: %w", err)
		}
	}
	changed := false
	for _, p := range cleaned {
		if _, decided := store[p].(bool); decided {
			continue
		}
		store[p] = true
		changed = true
	}
	if !changed {
		return nil
	}
	// Pi writes the store as sorted, two-space JSON; json.MarshalIndent sorts keys.
	body, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	dest := path.Join(home, ".pi", "agent", "trust.json")
	if err := remote.WriteFile(ctx, dest, append(body, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}
