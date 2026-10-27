package usecase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/graphify"
	"github.com/bouwerp/aiman/internal/quality"
)

// KitInput is what BuildQualityKit needs to know about one session.
type KitInput struct {
	Repo, Worktree string
	Role           domain.SessionRole
	Issue          *domain.Issue
	RepoCfg        config.RepoBootstrap
	// Restart leaves DoneWhen nil so the task file keeps its Done-when block.
	Restart bool
}

// shouldBuildKit reports whether a create gets a quality kit: repository
// sessions only, never ad-hoc, no-repo or reviewer sessions.
func shouldBuildKit(cfg domain.SessionConfig) bool {
	if cfg.Repo.Name == "" || cfg.Repo.Name == "No Repository" {
		return false
	}
	return !cfg.AdHoc && cfg.Role != domain.SessionRoleReviewer
}

// BuildQualityKit lists the repo and user checklists on the session host,
// keeps those whose globs match a file in the worktree, and merges the
// issue's acceptance criteria with the repo's done_when. It runs two
// Execute calls (list, match). Unreadable checklists and failed calls become
// progress notes; they never fail the create.
func BuildQualityKit(ctx context.Context, remote domain.RemoteExecutor, in KitInput) (domain.QualityKit, []string, error) {
	kit := domain.QualityKit{Repo: in.Repo, Role: in.Role}
	if !in.Restart {
		var ac []string
		if in.Issue != nil {
			ac = in.Issue.AcceptanceCriteria
		}
		kit.DoneWhen = quality.DoneWhen(ac, in.RepoCfg.DoneWhen)
	}
	if remote == nil || strings.TrimSpace(in.Worktree) == "" {
		return kit, nil, nil
	}

	var notes []string
	out, err := remote.Execute(ctx, quality.ListScript(in.Worktree))
	if err != nil {
		return kit, []string{fmt.Sprintf("Checklists not listed: %v", err)}, nil
	}
	_, raw := quality.ParseListOutput(out)
	var all []domain.Checklist
	for _, r := range raw {
		c, err := quality.ParseChecklist(r.Name(), r.Path, r.Source, r.Content)
		if err != nil {
			notes = append(notes, fmt.Sprintf("Skipped checklist %s: %v", r.Path, err))
			continue
		}
		all = append(all, c)
	}

	matched := map[string]bool{}
	if script := quality.MatchScript(in.Worktree, all); script != "" {
		out, err := remote.Execute(ctx, script)
		if err != nil {
			notes = append(notes, fmt.Sprintf("Checklist globs not matched: %v", err))
		} else {
			matched = quality.ParseMatchOutput(out)
		}
	}
	kit.Checklists = quality.Select(all, matched)
	return kit, notes, nil
}

const missingPrefix = "MISSING "

// cannotCheckSuffix marks an MCP requirement for an agent whose MCP config
// aiman does not know how to read.
const cannotCheckSuffix = " (cannot check for this agent)"

// mcpConfig says where an agent keeps its MCP servers and how a server name
// appears there.
type mcpConfig struct {
	files []string                 // shell words, already quoted
	keyRE func(name string) string // ERE matching the server's key
}

func jsonKeyRE(name string) string {
	return `"` + regexp.QuoteMeta(name) + `"[[:space:]]*:`
}

func tomlTableRE(name string) string {
	return `^[[:space:]]*\[mcp_servers\."?` + regexp.QuoteMeta(name) + `"?\]`
}

// mcpConfigFor returns the MCP config locations of the agent a command
// launches, or false when they are unknown.
func mcpConfigFor(worktree, agentCommand string) (mcpConfig, bool) {
	fields := strings.Fields(strings.ToLower(agentCommand))
	if len(fields) == 0 {
		return mcpConfig{}, false
	}
	base := path.Base(strings.Trim(fields[0], `"'`))
	switch {
	case strings.Contains(base, "claude"):
		return mcpConfig{files: []string{`"$HOME"/.claude.json`, shellQuote(filepath.Join(worktree, ".mcp.json"))}, keyRE: jsonKeyRE}, true
	case base == "codex":
		return mcpConfig{files: []string{`"$HOME"/.codex/config.toml`}, keyRE: tomlTableRE}, true
	case strings.Contains(base, "cursor"):
		return mcpConfig{files: []string{`"$HOME"/.cursor/mcp.json`}, keyRE: jsonKeyRE}, true
	case base == "gemini" || base == "agy" || base == "antigravity":
		return mcpConfig{files: []string{`"$HOME"/.gemini/settings.json`}, keyRE: jsonKeyRE}, true
	}
	return mcpConfig{}, false
}

// CheckRequires returns the repo's required tools that the session host
// lacks, in config order, using one Execute. A binary is looked up with
// command -v; an "mcp:<name>" entry is present when <name> is a key in the
// launched agent's MCP config. For agents whose MCP config is unknown the
// entry is reported with " (cannot check for this agent)".
func CheckRequires(ctx context.Context, remote domain.RemoteExecutor, worktree, agentCommand string, requires []string) ([]string, error) {
	mcp, mcpKnown := mcpConfigFor(worktree, agentCommand)
	var sb strings.Builder
	for _, entry := range requires {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		report := fmt.Sprintf("printf '%s%%s\\n' %s", missingPrefix, shellQuote(entry))
		if name, ok := strings.CutPrefix(entry, "mcp:"); ok {
			if !mcpKnown {
				continue
			}
			var greps []string
			for _, f := range mcp.files {
				greps = append(greps, fmt.Sprintf("grep -qE %s %s 2>/dev/null", shellQuote(mcp.keyRE(name)), f))
			}
			fmt.Fprintf(&sb, "{ %s; } || %s\n", strings.Join(greps, " || "), report)
			continue
		}
		fmt.Fprintf(&sb, "command -v %s >/dev/null 2>&1 || %s\n", shellQuote(entry), report)
	}

	missingSet := map[string]bool{}
	if sb.Len() > 0 {
		out, err := remote.Execute(ctx, graphify.PathPrefix+"\n"+sb.String()+"true")
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(out, "\n") {
			if e, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), missingPrefix); ok {
				missingSet[e] = true
			}
		}
	}

	var missing []string
	for _, entry := range requires {
		entry = strings.TrimSpace(entry)
		switch {
		case entry == "":
		case strings.HasPrefix(entry, "mcp:") && !mcpKnown:
			missing = append(missing, entry+cannotCheckSuffix)
		case missingSet[entry]:
			missing = append(missing, entry)
		}
	}
	return missing, nil
}

// PushUserChecklists copies the laptop's *.md checklists in localDir to the
// remote user checklist directory. One listing call fetches the remote
// sha256 of every checklist, and only new or changed files are written. It
// never deletes remote files. A missing localDir pushes nothing.
func PushUserChecklists(ctx context.Context, remote domain.RemoteExecutor, localDir string) (int, error) {
	entries, err := os.ReadDir(localDir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	local := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(localDir, e.Name())) // #nosec G304 -- the user's own checklist dir
		if err != nil {
			return 0, err
		}
		local[e.Name()] = b
	}
	if len(local) == 0 {
		return 0, nil
	}

	out, err := remote.Execute(ctx, quality.RemoteSumsScript())
	if err != nil {
		return 0, fmt.Errorf("list remote checklists: %w", err)
	}
	home, sums := quality.ParseRemoteSums(out)
	if home == "" {
		return 0, errors.New("list remote checklists: no home directory in output")
	}
	pushed := 0
	for _, name := range quality.PushPlan(local, sums) {
		if err := remote.WriteFile(ctx, path.Join(home, quality.UserChecklistDir, name), local[name]); err != nil {
			return pushed, fmt.Errorf("push checklist %s: %w", name, err)
		}
		pushed++
	}
	return pushed, nil
}
