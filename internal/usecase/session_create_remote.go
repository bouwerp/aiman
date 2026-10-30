package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

// RemoteCreateSpec is what the wizard decided, in the form serve's
// session.create accepts.
type RemoteCreateSpec struct {
	Name   string
	Group  string
	Repo   string
	Branch string
	Agent  string
	// Tier is the model tier to launch from when Agent is empty: smart or
	// worker. An empty tier is serve's default (smart for a user-started
	// session, worker for a child).
	Tier string
	// Model and Effort override the launch for an explicit Agent.
	Model          string
	Effort         string
	Dir            string
	Prompt         string
	Issue          string
	BaseBranch     string
	AgentName      string
	Quick          bool
	ExistingBranch bool
	// PromptFree suppresses the task file the skill engine writes from the JIRA
	// issue. serve resolves the issue from its key itself, so a task-driven
	// session needs only this flag and the key.
	PromptFree bool
	// Review is "owner/repo#n": the session reviews that pull request. When
	// set, repo, branch, dir, prompt, issue and base are omitted; serve
	// refuses those together with review.
	Review string
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
	// An empty agent is left off, so serve picks from the model tiers; an
	// explicit tier with no agent is how the caller says which tier to try.
	params := map[string]any{}
	for key, val := range map[string]string{
		"agent":      spec.Agent,
		"tier":       spec.Tier,
		"model":      spec.Model,
		"effort":     spec.Effort,
		"name":       spec.Name,
		"group":      spec.Group,
		"agent_name": spec.AgentName,
		"review":     spec.Review,
	} {
		if strings.TrimSpace(val) != "" {
			params[key] = val
		}
	}
	if strings.TrimSpace(spec.Review) == "" {
		for key, val := range map[string]string{
			"repo":   spec.Repo,
			"branch": spec.Branch,
			"dir":    spec.Dir,
			"prompt": spec.Prompt,
			"issue":  spec.Issue,
			"base":   spec.BaseBranch,
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
	sess, err := parseRemoteCreateResult(out)
	if err != nil {
		return nil, err
	}
	if sess.ReviewPR == "" {
		sess.ReviewPR = spec.Review
	}
	return sess, nil
}

// ForgetSessionOnRemote removes the session from the remote serve's own
// database, and drops whatever its PTY runtime still holds for it.
//
// The dashboard deletes its local row on teardown, but serve keeps a separate
// database and the dashboard rebuilds its list from serve's live stream — so
// without this a terminated session reappeared on the next poll and ctrl+k
// looked like it had done nothing.
//
// Best-effort by design: a remote with no serve running, or one too old to know
// the method, must not fail a teardown that has already killed the terminal.
func ForgetSessionOnRemote(ctx context.Context, remote TerminalExecutor, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	out, err := remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf("aiman session forget %q", sessionID))
	if err == nil {
		return nil
	}
	// "not found" is the goal state, and a remote without serve cannot be
	// holding the row in the first place.
	combined := strings.ToLower(strings.TrimSpace(out) + " " + err.Error())
	for _, benign := range []string{"not_found", "server_not_running", "unknown method", "command not found"} {
		if strings.Contains(combined, benign) {
			return nil
		}
	}
	return fmt.Errorf("remote forget: %s: %w", strings.TrimSpace(out), err)
}

// RegisterSessionsOnRemote hands sessions the laptop owns to the remote's serve
// so it persists them and can accept their hook reports.
//
// A remote whose serve is stopped or too old to know the method is not an
// error: registration is retried the next time the dashboard syncs.
func RegisterSessionsOnRemote(ctx context.Context, remote TerminalExecutor, sessions []domain.Session) error {
	if len(sessions) == 0 {
		return nil
	}
	_, err := ServeRPC(ctx, remote, "session.register", map[string]any{"sessions": sessions})
	if errors.Is(err, ErrRPCUnsupported) || errors.Is(err, ErrServeNotRunning) {
		return nil
	}
	return err
}

// remoteCreateResult is what `aiman session create` prints: the bare
// session.create result on success, or the {"error":…} line on failure.
type remoteCreateResult struct {
	Session struct {
		ID               string `json:"id"`
		Name             string `json:"name"`
		Group            string `json:"group"`
		IssueKey         string `json:"issue_key"`
		Branch           string `json:"branch"`
		BaseBranch       string `json:"base_branch"`
		RepoName         string `json:"repo_name"`
		TerminalName     string `json:"tmux_session"`
		Backend          string `json:"backend"`
		WorktreePath     string `json:"worktree_path"`
		WorkingDirectory string `json:"working_directory"`
		AgentName        string `json:"agent_name"`
		LaunchModel      string `json:"launch_model"`
		LaunchEffort     string `json:"launch_effort"`
		Tier             string `json:"tier"`
		ReviewPR         string `json:"review_pr"`
	} `json:"session"`
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
	s := res.Session
	if strings.TrimSpace(s.ID) == "" {
		return nil, fmt.Errorf("remote create: response carried no session id")
	}
	return &domain.Session{
		ID:               s.ID,
		Name:             s.Name,
		Group:            s.Group,
		IssueKey:         s.IssueKey,
		Branch:           s.Branch,
		BaseBranch:       s.BaseBranch,
		RepoName:         s.RepoName,
		TerminalName:     s.TerminalName,
		Backend:          s.Backend,
		WorktreePath:     s.WorktreePath,
		WorkingDirectory: s.WorkingDirectory,
		AgentName:        s.AgentName,
		AgentModel:       s.LaunchModel,
		LaunchModel:      s.LaunchModel,
		LaunchEffort:     s.LaunchEffort,
		Tier:             s.Tier,
		ReviewPR:         s.ReviewPR,
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
