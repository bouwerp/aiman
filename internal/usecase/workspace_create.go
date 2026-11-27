package usecase

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/aimanskill"
	"github.com/bouwerp/aiman/internal/domain"
	infraGit "github.com/bouwerp/aiman/internal/infra/git"
	"github.com/bouwerp/aiman/internal/infra/remotefile"
	"github.com/bouwerp/aiman/internal/shellx"
)

// WorkspaceDirSuffix ends the directory name of a multi-repo session:
// <root>/<branch>@ws holds one worktree per member.
const WorkspaceDirSuffix = "@ws"

// WorkspaceDir is the workspace directory of a multi-repo session on branch.
func WorkspaceDir(root, branch string) string {
	return strings.TrimRight(root, "/") + "/" + branch + WorkspaceDirSuffix
}

// workspaceMemberDirs names each member's directory inside the workspace:
// the repo name, or <owner>-<name> for every repo whose name repeats.
func workspaceMemberDirs(specs []domain.RepoSpec) []string {
	count := map[string]int{}
	for _, s := range specs {
		count[strings.ToLower(path.Base(s.Repo))]++
	}
	dirs := make([]string, len(specs))
	for i, s := range specs {
		name := path.Base(s.Repo)
		if count[strings.ToLower(name)] > 1 {
			name = strings.ReplaceAll(s.Repo, "/", "-")
		}
		dirs[i] = name
	}
	return dirs
}

// createWorkspaceSession is CreateSession for a config with Workspace set:
// it checks the branch is free in every member and the workspace directory
// does not exist, makes the directory and one worktree per member, then
// launches the agent in the directory. Any failure removes the members made
// so far and the directory.
func (m *FlowManager) createWorkspaceSession(ctx context.Context, sshMgr domain.RemoteExecutor, config domain.SessionConfig, session *domain.Session) (*domain.Session, error) {
	wg, ok := m.gitManager.(domain.WorkspaceGit)
	if !ok {
		return nil, errors.New("multi-repo sessions are not supported by this git manager")
	}
	branch := session.Branch
	if branch == "" {
		return nil, errors.New("a multi-repo session needs a branch or a name")
	}
	ws := WorkspaceDir(sshMgr.GetRoot(), branch)
	if err := domain.CheckLaunchDir(ws); err != nil {
		return nil, err
	}

	infraGit.ReportProgress(ctx, "Checking the branch is free in every repository…")
	for _, spec := range config.Workspace {
		busy, err := wg.BranchCheckedOut(ctx, sshMgr, domain.Repo{Name: spec.Repo}, branch)
		if err != nil {
			return nil, fmt.Errorf("check branch %s in %s: %w", branch, spec.Repo, err)
		}
		if busy {
			return nil, fmt.Errorf("WORKTREE_EXISTS: branch %s is checked out in %s", branch, spec.Repo)
		}
	}
	out, err := sshMgr.Execute(ctx, fmt.Sprintf("if [ -e %s ]; then echo EXISTS; fi", shellx.Quote(ws)))
	if err != nil {
		return nil, fmt.Errorf("check workspace directory: %w", err)
	}
	if strings.Contains(out, "EXISTS") {
		return nil, fmt.Errorf("WORKTREE_EXISTS: %s already exists", ws)
	}
	if _, err := sshMgr.Execute(ctx, fmt.Sprintf("mkdir -p %s && mkdir %s", shellx.Quote(filepath.Dir(ws)), shellx.Quote(ws))); err != nil {
		return nil, fmt.Errorf("create workspace directory: %w", err)
	}

	var made []domain.SessionRepo
	rollback := func() {
		// The create's own context may be the one that failed.
		rctx := context.WithoutCancel(ctx)
		for i := len(made) - 1; i >= 0; i-- {
			if err := wg.RemoveWorkspaceMember(rctx, sshMgr, domain.Repo{Name: made[i].Repo}, made[i].WorktreePath); err != nil {
				infraGit.ReportProgress(ctx, fmt.Sprintf("Member %s not removed: %v", made[i].Repo, err))
			}
		}
		if _, err := sshMgr.Execute(rctx, WorkspaceRemoveDirScript(ws)); err != nil {
			infraGit.ReportProgress(ctx, fmt.Sprintf("Workspace directory %s not removed: %v", ws, err))
		}
	}

	var setupNotes []string
	dirs := workspaceMemberDirs(config.Workspace)
	for i, spec := range config.Workspace {
		repo := domain.Repo{Name: spec.Repo}
		memberPath := ws + "/" + dirs[i]
		infraGit.ReportProgress(ctx, fmt.Sprintf("Adding %s…", spec.Repo))
		wt, err := wg.SetupWorkspaceMember(ctx, sshMgr, repo, branch, spec.Base, memberPath)
		if err != nil {
			rollback()
			return nil, fmt.Errorf("set up %s: %w", spec.Repo, err)
		}
		made = append(made, domain.SessionRepo{
			Repo:         spec.Repo,
			Branch:       branch,
			BaseBranch:   sessionBaseBranch(spec.Base, wt.Base),
			WorktreePath: memberPath,
			Ord:          i,
		})
		memberCfg := config
		memberCfg.Workspace = nil
		memberCfg.Repo = repo
		if setup, logPath := m.applyBootstrap(ctx, sshMgr, memberCfg, memberPath); logPath != "" {
			setupNotes = append(setupNotes, bootstrapPromptNote(setup, logPath))
		}
		_ = m.gitManager.EnsureAimanSessionFilesGitignored(ctx, sshMgr, memberPath)
		if _, err := sshMgr.Execute(ctx, workspaceMarkerScript(memberPath, session.ID)); err != nil {
			rollback()
			return nil, fmt.Errorf("mark %s as a workspace member: %w", spec.Repo, err)
		}
		_, _ = sshMgr.Execute(ctx, "git config --global --add safe.directory "+shellx.Quote(memberPath))
	}

	session.WorktreePath = ws
	session.WorkingDirectory = ws
	session.RepoName = ""
	session.BaseBranch = ""
	session.Members = made

	sess, err := m.launchWorkspace(ctx, sshMgr, config, session, setupNotes)
	if err != nil {
		rollback()
		return nil, err
	}
	return sess, nil
}

// launchWorkspace writes the session files into the workspace directory,
// trusts it and starts the agent there.
func (m *FlowManager) launchWorkspace(ctx context.Context, sshMgr domain.RemoteExecutor, config domain.SessionConfig, session *domain.Session, setupNotes []string) (*domain.Session, error) {
	dir := session.WorkingDirectory
	agentCmd := config.Agent.Command
	var prompt string
	var kit domain.QualityKit
	if m.SkillEngine != nil {
		built := m.buildWorkspaceKit(ctx, sshMgr, config, dir, session.Members, config.Agent.Command)
		prepared, err := m.SkillEngine.PrepareSession(ctx, sshMgr, dir, *config.Agent, config.Skills, config.PromptFree, config.Issue, config.PriorSnapshot, built)
		if err == nil {
			agentCmd = prepared.Command
			prompt = prepared.InitialPrompt
			kit = built
		}
	}
	if err := WriteWorkspaceRepositories(ctx, sshMgr, dir, session.Members); err != nil {
		return nil, fmt.Errorf("failed to write the repositories to the task file: %w", err)
	}
	if config.Brief != "" {
		if err := WriteTaskBrief(ctx, sshMgr, dir, config.Brief); err != nil {
			return nil, fmt.Errorf("failed to write the brief: %w", err)
		}
	}
	if !strings.Contains(prompt, domain.AimanTaskFileName) {
		prompt = joinPrompt(prompt, "Read "+domain.AimanTaskFileName+" first: it lists the repositories of this session.")
	}
	slugs := make([]string, len(session.Members))
	for i, mem := range session.Members {
		slugs[i] = mem.Repo
	}
	prompt = InjectWorkspaceContext(ctx, sshMgr, dir, session.Group, slugs, prompt)
	prompt = joinPrompt(prompt, config.InitialPrompt)
	for _, n := range setupNotes {
		prompt = joinPrompt(prompt, n)
	}
	prompt = designFirstPrompt(prompt, config)
	WriteMuseSessionID(ctx, sshMgr, dir, agentCmd, session.ID)

	awsEnv := map[string]string{}
	if config.AWSConfig != nil {
		awsEnv = SharedSessionAWSEnv(config.AWSConfig.SourceProfile, config.AWSConfig.Region)
		session.AWSConfig = config.AWSConfig
	}
	infraGit.ReportProgress(ctx, "Trusting workspace…")
	m.trustWorkspaceBeforeLaunch(ctx, sshMgr, dir, "")

	return m.launchPTYSession(ctx, sshMgr, session, config, kit, dir, terminalNameForCreate(session.Branch, session.Name), agentCmd, prompt, awsEnv)
}

// workspaceMarkerScript writes the session id into the member's git dir as
// domain.WorkspaceMarkerFile, so discovery never offers the member as a
// session of its own.
func workspaceMarkerScript(memberPath, id string) string {
	return fmt.Sprintf("f=$(git -C %s rev-parse --absolute-git-dir)/%s && echo %s > \"$f\"",
		shellx.Quote(memberPath), domain.WorkspaceMarkerFile, shellx.Quote(id))
}

// WorkspaceRemoveDirScript removes the scaffolding a create wrote into
// the workspace directory, then the directory. rmdir leaves anything else in
// place.
func WorkspaceRemoveDirScript(ws string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "rm -f -- %s/.aiman_*; ", shellx.Quote(ws))
	for _, s := range aimanskill.Skills() {
		for _, d := range aimanskill.ProjectSkillDirs(ws, s.Name) {
			fmt.Fprintf(&b, "rm -rf -- %s; rmdir -- %s %s 2>/dev/null; ", shellx.Quote(d), shellx.Quote(path.Dir(d)), shellx.Quote(path.Dir(path.Dir(d))))
		}
	}
	fmt.Fprintf(&b, "rmdir -- %s", shellx.Quote(ws))
	return b.String()
}

// workspaceReposHeading opens the task file's list of member repositories.
const workspaceReposHeading = "## Repositories"

// WorkspaceRepositoriesSection is the task file section listing a workspace
// session's members and how to work across them.
func WorkspaceRepositoriesSection(members []domain.SessionRepo) string {
	var b strings.Builder
	b.WriteString(workspaceReposHeading + "\n\n")
	b.WriteString("This session spans several repositories. Each subdirectory below is its own git repository:\n\n")
	for _, mem := range members {
		base := mem.BaseBranch
		if base == "" {
			base = "default branch"
		}
		fmt.Fprintf(&b, "- `%s`: branch `%s` from `%s`, at `%s`\n", mem.Repo, mem.Branch, base, mem.WorktreePath)
	}
	b.WriteString("\nRules:\n\n")
	b.WriteString("- Read each repository's CLAUDE.md or AGENTS.md before you edit it.\n")
	b.WriteString("- Commit and push in each repository on its own; open one PR per repository.\n")
	b.WriteString("- Name the repository in every command, as a simple command: push with `git -C <member dir> push`, open a PR with `gh -R <owner/repo> pr create`, run local CI with `aiman ci local --repo <owner/repo>`. A plain `git push` or `gh pr create` is refused here.\n")
	return b.String()
}

// WriteWorkspaceRepositories puts the repositories section at the top of
// dir's task file, after the scaffold header and before anything already
// there. A file that already holds the section is left as it is.
func WriteWorkspaceRepositories(ctx context.Context, remote remotefile.Writer, dir string, members []domain.SessionRepo) error {
	p := filepath.Join(dir, domain.AimanTaskFileName)
	out, err := remote.Execute(ctx, fmt.Sprintf("echo %s; cat -- %s 2>/dev/null || true", taskBriefMarker, shellx.Quote(p)))
	if err != nil {
		return fmt.Errorf("read %s: %w", domain.AimanTaskFileName, err)
	}
	_, existing, _ := strings.Cut(out, taskBriefMarker+"\n")
	body := strings.TrimPrefix(existing, domain.AimanScaffoldHeader)
	if strings.Contains(body, workspaceReposHeading+"\n") {
		return nil
	}
	body = joinTaskSections(WorkspaceRepositoriesSection(members), body)
	_, err = remotefile.WriteIfChanged(ctx, remote, p, []byte(domain.AimanScaffoldHeader+body))
	return err
}
