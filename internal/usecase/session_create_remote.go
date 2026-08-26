package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

// RemoteCreateSpec is what the wizard decided, in the form serve's
// session.create accepts.
type RemoteCreateSpec struct {
	Name           string
	Group          string
	Repo           string
	Branch         string
	Agent          string
	Dir            string
	Prompt         string
	Issue          string
	BaseBranch     string
	Backend        string
	AgentName      string
	Quick          bool
	ExistingBranch bool
	// PromptFree suppresses the task file the skill engine writes from the JIRA
	// issue. serve resolves the issue from its key itself, so a task-driven
	// session needs only this flag and the key.
	PromptFree bool
}

// CreateSessionOnRemote asks the remote's own aiman serve to create the session.
//
// The point is that the work does not belong to this process. serve builds a
// full FlowManager over a local executor, so the worktree, the agent launch and
// the prompt all happen on the remote, and the flow is not tied to the
// connection that asked for it — the caller can exit the moment this returns,
// and can exit before it returns without stopping the create.
//
// Two things cannot follow it across: the mutagen sync is created by a mutagen
// daemon on *this* machine, and delegated AWS credentials are minted from this
// machine's ~/.aws. Both are reconciled by the dashboard when it next sees the
// session.
func CreateSessionOnRemote(ctx context.Context, remote TerminalExecutor, spec RemoteCreateSpec) (*domain.Session, error) {
	if strings.TrimSpace(spec.Agent) == "" {
		return nil, fmt.Errorf("remote create: agent is required")
	}
	params := map[string]any{"agent": spec.Agent}
	for key, val := range map[string]string{
		"name":       spec.Name,
		"group":      spec.Group,
		"repo":       spec.Repo,
		"branch":     spec.Branch,
		"dir":        spec.Dir,
		"prompt":     spec.Prompt,
		"issue":      spec.Issue,
		"base":       spec.BaseBranch,
		"backend":    spec.Backend,
		"agent_name": spec.AgentName,
	} {
		if strings.TrimSpace(val) != "" {
			params[key] = val
		}
	}
	if spec.Quick {
		params["quick"] = true
	}
	if spec.ExistingBranch {
		params["existing_branch"] = true
	}
	// Always explicit: absent means "true" on the wire, and a task-driven session
	// needs it false.
	params["prompt_free"] = spec.PromptFree

	// The params travel as a file so nothing in them — a prompt above all — is
	// ever parsed by a shell.
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("remote create: encode params: %w", err)
	}
	path := fmt.Sprintf("/tmp/aiman-create-%s.json", sanitiseTempName(spec.Branch+spec.Name))
	if err := remote.WriteFile(ctx, path, raw); err != nil {
		return nil, fmt.Errorf("remote create: write params: %w", err)
	}

	out, err := remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf(
		"aiman session create --params-file %q; rm -f %q", path, path))
	if err != nil {
		return nil, fmt.Errorf("remote create: %s: %w", strings.TrimSpace(out), err)
	}
	return parseRemoteCreateResult(out)
}

// remoteCreateResult is the shape session.create answers with.
type remoteCreateResult struct {
	Result struct {
		Session struct {
			ID               string `json:"id"`
			Name             string `json:"name"`
			Group            string `json:"group"`
			IssueKey         string `json:"issue_key"`
			Branch           string `json:"branch"`
			RepoName         string `json:"repo_name"`
			TmuxSession      string `json:"tmux_session"`
			WorktreePath     string `json:"worktree_path"`
			WorkingDirectory string `json:"working_directory"`
			AgentName        string `json:"agent_name"`
		} `json:"session"`
	} `json:"result"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func parseRemoteCreateResult(out string) (*domain.Session, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, fmt.Errorf("remote create: no response")
	}
	var res remoteCreateResult
	if err := json.Unmarshal([]byte(trimmed), &res); err != nil {
		return nil, fmt.Errorf("remote create: unexpected response %q", truncateForError(trimmed))
	}
	if res.Error != nil {
		return nil, fmt.Errorf("remote create: %s", res.Error.Message)
	}
	s := res.Result.Session
	if strings.TrimSpace(s.ID) == "" {
		return nil, fmt.Errorf("remote create: response carried no session id")
	}
	return &domain.Session{
		ID:               s.ID,
		Name:             s.Name,
		Group:            s.Group,
		IssueKey:         s.IssueKey,
		Branch:           s.Branch,
		RepoName:         s.RepoName,
		TmuxSession:      s.TmuxSession,
		WorktreePath:     s.WorktreePath,
		WorkingDirectory: s.WorkingDirectory,
		AgentName:        s.AgentName,
		Status:           domain.SessionStatusActive,
	}, nil
}

// sanitiseTempName keeps a temp filename to characters that need no quoting and
// cannot escape /tmp.
func sanitiseTempName(v string) string {
	var b strings.Builder
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= 48 {
			break
		}
	}
	if b.Len() == 0 {
		return "session"
	}
	return b.String()
}

func truncateForError(v string) string {
	if len(v) <= 200 {
		return v
	}
	return v[:200] + "…"
}
