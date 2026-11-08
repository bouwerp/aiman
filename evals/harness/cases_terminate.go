package harness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// evalGit runs git in dir with a fixed identity.
func evalGit(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // G204: fixed git fixture commands in the eval harness
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=e", "GIT_AUTHOR_EMAIL=e@example.com", "GIT_COMMITTER_NAME=e", "GIT_COMMITTER_EMAIL=e@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.New(string(out) + err.Error())
	}
	return nil
}

// evalRepo makes a main checkout pushed to a bare origin, and a linked worktree
// on branch feature, under the harness directory.
func evalRepo(h *Harness) (mainDir, linked string, err error) {
	base, err := filepath.EvalSymlinks(h.Dir)
	if err != nil {
		return "", "", err
	}
	mainDir = filepath.Join(base, "repo")
	linked = filepath.Join(base, "repo@feature")
	origin := filepath.Join(base, "origin.git")
	if err := os.MkdirAll(mainDir, 0o750); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(mainDir, "a.txt"), []byte("a\n"), 0o600); err != nil {
		return "", "", err
	}
	for _, step := range []struct {
		dir  string
		args []string
	}{
		{base, []string{"init", "--bare", "-q", origin}},
		{mainDir, []string{"init", "-q", "-b", "main"}},
		{mainDir, []string{"add", "."}},
		{mainDir, []string{"commit", "-qm", "init"}},
		{mainDir, []string{"remote", "add", "origin", origin}},
		{mainDir, []string{"push", "-q", "origin", "main"}},
		{mainDir, []string{"worktree", "add", "-q", "-b", "feature", linked}},
	} {
		if err := evalGit(step.dir, step.args...); err != nil {
			return "", "", err
		}
	}
	return mainDir, linked, nil
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	code, _, _ := strings.Cut(err.Error(), ":")
	return code
}

func removedFlag(resp map[string]any) bool {
	removed, _ := resp["worktree_removed"].(bool)
	return removed
}

func resultOf(result any) map[string]any {
	m, _ := result.(map[string]any)
	return m
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// CaseTerminateWorktree walks session.terminate through a real repository: a
// session cannot end itself or a session outside its tree, a dirty linked worktree is refused with nothing
// stopped, discard removes it and keeps the branch, and a session on the main
// checkout never loses it.
func CaseTerminateWorktree() Case {
	return Case{Name: "terminate-worktree", Assert: func(h *Harness) []MetricResult {
		mainDir, linked, err := evalRepo(h)
		if err != nil {
			return []MetricResult{score("fixture", false, err.Error())}
		}
		h.RegisterSession(domain.Session{ID: "s1", WorktreePath: linked, WorkingDirectory: linked, Branch: "feature"})
		h.RegisterSession(domain.Session{ID: "s2", WorktreePath: mainDir, WorkingDirectory: mainDir, Branch: "main"})
		dirty := filepath.Join(linked, "work.txt")
		if err := os.WriteFile(dirty, []byte("work\n"), 0o600); err != nil {
			return []MetricResult{score("fixture", false, err.Error())}
		}
		mainDirty := filepath.Join(mainDir, "main-work.txt")
		if err := os.WriteFile(mainDirty, []byte("work\n"), 0o600); err != nil {
			return []MetricResult{score("fixture", false, err.Error())}
		}

		_, asSelf := h.CallAs("s1", "session.terminate", map[string]any{"session": "s1", "discard": true})
		_, asSession := h.CallAs("s2", "session.terminate", map[string]any{"session": "s1", "discard": true})
		_, refused := h.CallAs("", "session.terminate", map[string]any{"session": "s1"})
		stillThere := fileExists(dirty) && !slices.Contains(h.Kills(), "s1")

		resp, derr := h.CallAs("", "session.terminate", map[string]any{"session": "s1", "discard": true})
		discarded := derr == nil && removedFlag(resultOf(resp.Result)) && !fileExists(linked)
		branchKept := evalGit(mainDir, "rev-parse", "--verify", "refs/heads/feature") == nil
		ended := slices.ContainsFunc(h.Events("s1"), func(e domain.SessionEvent) bool { return e.Kind == domain.EventSessionEnded })

		mainResp, merr := h.CallAs("", "session.terminate", map[string]any{"session": "s2", "discard": true})
		mainKept := merr == nil && !removedFlag(resultOf(mainResp.Result)) && fileExists(mainDirty) && fileExists(filepath.Join(mainDir, ".git"))
		return []MetricResult{
			score("terminate_needs_operator", errorCode(asSelf) == "needs_operator", "self: "+errString(asSelf)),
			score("terminate_outside_tree", errorCode(asSession) == "session_scope", "unrelated session: "+errString(asSession)),
			score("terminate_refuses_dirty", errorCode(refused) == "worktree_dirty", "dirty: "+errString(refused)),
			score("terminate_refusal_stops_nothing", stillThere, "the refusal touched the session"),
			score("terminate_discard_removes", discarded, "discard: "+errString(derr)),
			score("terminate_keeps_branch", branchKept, "branch deleted"),
			score("terminate_records_end", ended, "no session.ended"),
			score("terminate_keeps_main_checkout", mainKept, "main checkout: "+errString(merr)),
		}
	}}
}

func errString(err error) string {
	if err == nil {
		return "no error"
	}
	return err.Error()
}

// CaseRestartSwitch switches a session's agent: the handover goes first and
// names the switch, the vendor identity and launch model are wiped, and the
// agent is recreated under the same id. The worktree is not touched.
func CaseRestartSwitch() Case {
	return Case{Name: "restart-switch", Assert: func(h *Harness) []MetricResult {
		dir := filepath.Join(h.Dir, "work")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return []MetricResult{score("fixture", false, err.Error())}
		}
		keep := filepath.Join(dir, "work.txt")
		if err := os.WriteFile(keep, []byte("work\n"), 0o600); err != nil {
			return []MetricResult{score("fixture", false, err.Error())}
		}
		h.RegisterSession(domain.Session{
			ID: "s1", WorktreePath: dir, WorkingDirectory: dir, AgentName: "claude",
			AgentSessionID: "vendor-1", LaunchModel: "old-model",
		})
		h.srv.SetAgentChoice(config.AgentsConfig{}, func(_ context.Context, command string) bool { return command == "codex" })

		_, notInstalled := h.CallAs("", "session.restart", map[string]any{"session": "s1", "agent": "grok"})
		_, asSession := h.CallAs("s1", "session.restart", map[string]any{"session": "s1", "agent": "codex"})
		created := func() bool {
			return slices.ContainsFunc(h.Execs(), func(c string) bool { return strings.Contains(c, "pty create") })
		}
		refusalsIdle := !created()

		resp, err := h.CallAs("", "session.restart", map[string]any{"session": "s1", "agent": "codex"})
		if err != nil {
			return []MetricResult{score("restart_ok", false, err.Error())}
		}
		raw, _ := json.Marshal(resp.Result)
		var out struct {
			Type    string `json:"type"`
			Session struct {
				ID             string `json:"id"`
				AgentName      string `json:"agent_name"`
				AgentSessionID string `json:"agent_session_id"`
				LaunchModel    string `json:"launch_model"`
			} `json:"session"`
		}
		decodeErr := json.Unmarshal(raw, &out)
		var handoverReason bool
		for _, e := range h.Events("s1") {
			if e.Kind == domain.EventHandoverWritten && strings.Contains(string(e.Payload), "agent_switch") {
				handoverReason = true
			}
		}
		return []MetricResult{
			score("restart_not_installed_refused", errorCode(notInstalled) == "invalid_params", errString(notInstalled)),
			score("restart_needs_operator", errorCode(asSession) == "needs_operator", errString(asSession)),
			score("restart_refusals_start_nothing", refusalsIdle, "a refusal recreated the terminal"),
			score("restart_same_id", decodeErr == nil && out.Type == "session" && out.Session.ID == "s1" && out.Session.AgentName == "codex", string(raw)),
			score("restart_clears_identity", out.Session.AgentSessionID == "" && out.Session.LaunchModel == "", string(raw)),
			score("restart_handover_names_switch", handoverReason, "no agent_switch handover on the ledger"),
			score("restart_recreates_terminal", created(), "no pty create"),
			score("worktree_untouched_on_restart", fileExists(keep), "the worktree changed"),
		}
	}}
}
