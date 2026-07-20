package usecase

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

type SSHManagerFactory func(host, user, root string) domain.RemoteExecutor

type EC2LoopRunner struct {
	ec2Manager domain.EC2Manager
	sshFactory SSHManagerFactory
}

func NewEC2LoopRunner(ec2Manager domain.EC2Manager, sshFactory SSHManagerFactory) *EC2LoopRunner {
	return &EC2LoopRunner{
		ec2Manager: ec2Manager,
		sshFactory: sshFactory,
	}
}

type EC2LoopProgress struct {
	Step    string `json:"step"`
	Message string `json:"message"`
}

func (r *EC2LoopRunner) Run(ctx context.Context, spec domain.EC2LaunchSpec, progress chan<- EC2LoopProgress) (res *domain.EC2LoopResult, err error) {
	startTime := time.Now()

	sendProgress := func(step, msg string) {
		if progress != nil {
			progress <- EC2LoopProgress{Step: step, Message: msg}
		}
	}

	if spec.SSHUser == "" {
		spec.SSHUser = "ubuntu"
	}
	if spec.AgentName == "" {
		spec.AgentName = "claude"
	}
	if spec.Branch == "" {
		if spec.IssueKey != "" {
			spec.Branch = fmt.Sprintf("feature/%s", spec.IssueKey)
		} else {
			spec.Branch = fmt.Sprintf("feature/auto-%d", time.Now().Unix())
		}
	}
	if spec.TimeoutMinutes <= 0 {
		spec.TimeoutMinutes = 60
	}

	sendProgress("ec2-launch", "Launching on-demand EC2 instance...")
	inst, err := r.ec2Manager.LaunchInstance(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("ec2 launch failed: %w", err)
	}

	selfDestructed := false
	defer func() {
		if spec.SelfDestruct && inst != nil && inst.InstanceID != "" && !selfDestructed {
			sendProgress("ec2-terminate", fmt.Sprintf("Self-destructing EC2 instance %s...", inst.InstanceID))
			_ = r.ec2Manager.TerminateInstance(context.Background(), spec.AWSProfile, spec.Region, inst.InstanceID)
			selfDestructed = true
		}
	}()

	sendProgress("ssh-wait", fmt.Sprintf("Waiting for instance %s SSH connection to become ready...", inst.InstanceID))
	inst, err = r.ec2Manager.WaitUntilSSHReady(ctx, spec.AWSProfile, spec.Region, inst.InstanceID, spec.SSHUser, spec.SSHKeyPath, 5*time.Minute)
	if err != nil {
		return &domain.EC2LoopResult{
			InstanceID:   inst.InstanceID,
			Success:      false,
			Error:        fmt.Sprintf("SSH wait failed: %v", err),
			Duration:     time.Since(startTime),
			SelfDestruct: spec.SelfDestruct,
		}, err
	}

	sshMgr := r.sshFactory(inst.PublicIP, spec.SSHUser, fmt.Sprintf("/home/%s", spec.SSHUser))
	defer sshMgr.Close()

	sendProgress("provision", "Provisioning remote software stack (git, node, gh, agents)...")
	provisioner := NewProvisioner(sshMgr)
	provChan := make(chan domain.ProvisionProgress, 20)
	go func() {
		for p := range provChan {
			sendProgress("provision-step", fmt.Sprintf("[%s] %s: %s", p.StepID, p.Status, p.Message))
		}
	}()

	if err := provisioner.Provision(ctx, provChan); err != nil {
		close(provChan)
		return &domain.EC2LoopResult{
			InstanceID:   inst.InstanceID,
			Success:      false,
			Error:        fmt.Sprintf("provisioning failed: %v", err),
			Duration:     time.Since(startTime),
			SelfDestruct: spec.SelfDestruct,
		}, err
	}
	close(provChan)

	sendProgress("credentials", "Injecting credentials and environment variables...")
	if err := r.injectCredentials(ctx, sshMgr, spec.EnvironmentVars); err != nil {
		return &domain.EC2LoopResult{
			InstanceID:   inst.InstanceID,
			Success:      false,
			Error:        fmt.Sprintf("credential injection failed: %v", err),
			Duration:     time.Since(startTime),
			SelfDestruct: spec.SelfDestruct,
		}, err
	}

	sendProgress("repositories", "Cloning repositories and creating feature branch...")
	primaryRepoDir, err := r.cloneRepositories(ctx, sshMgr, spec.Repositories, spec.Branch)
	if err != nil {
		return &domain.EC2LoopResult{
			InstanceID:   inst.InstanceID,
			Success:      false,
			Error:        fmt.Sprintf("repository setup failed: %v", err),
			Duration:     time.Since(startTime),
			SelfDestruct: spec.SelfDestruct,
		}, err
	}

	sendProgress("task-scaffold", "Writing task instructions and loop engineering prompt...")
	if err := r.scaffoldTask(ctx, sshMgr, primaryRepoDir, spec); err != nil {
		return &domain.EC2LoopResult{
			InstanceID:   inst.InstanceID,
			Success:      false,
			Error:        fmt.Sprintf("task scaffolding failed: %v", err),
			Duration:     time.Since(startTime),
			SelfDestruct: spec.SelfDestruct,
		}, err
	}

	sendProgress("agent-run", fmt.Sprintf("Launching autonomous agent (%s) loop...", spec.AgentName))
	prURL, runErr := r.runAutonomousLoop(ctx, sshMgr, primaryRepoDir, spec)
	if runErr != nil {
		return &domain.EC2LoopResult{
			InstanceID:   inst.InstanceID,
			PRURL:        prURL,
			Success:      false,
			Error:        runErr.Error(),
			Duration:     time.Since(startTime),
			SelfDestruct: spec.SelfDestruct,
		}, runErr
	}

	sendProgress("complete", fmt.Sprintf("Autonomous loop completed successfully. PR: %s", prURL))
	return &domain.EC2LoopResult{
		InstanceID:   inst.InstanceID,
		PRURL:        prURL,
		Success:      true,
		Duration:     time.Since(startTime),
		SelfDestruct: spec.SelfDestruct,
	}, nil
}

func (r *EC2LoopRunner) injectCredentials(ctx context.Context, sshMgr domain.RemoteExecutor, envVars map[string]string) error {
	var envLines []string
	for k, v := range envVars {
		envLines = append(envLines, fmt.Sprintf("export %s=%q", k, v))

		if k == "GITHUB_TOKEN" || k == "GH_TOKEN" {
			cmd := fmt.Sprintf("echo %q | gh auth login --with-token >/dev/null 2>&1 || true", v)
			_, _ = sshMgr.Execute(ctx, cmd)
			gitUserCmd := "git config --global user.name 'aiman-ec2-agent' && git config --global user.email 'aiman-agent@users.noreply.github.com'"
			_, _ = sshMgr.Execute(ctx, gitUserCmd)
		}
	}

	if len(envLines) > 0 {
		content := strings.Join(envLines, "\n") + "\n"
		cmd := fmt.Sprintf("printf %%s %q >> ~/.bashrc", content)
		_, err := sshMgr.Execute(ctx, cmd)
		if err != nil {
			return fmt.Errorf("writing env to bashrc: %w", err)
		}
	}
	return nil
}

func (r *EC2LoopRunner) cloneRepositories(ctx context.Context, sshMgr domain.RemoteExecutor, repos []string, branch string) (string, error) {
	if len(repos) == 0 {
		return "", fmt.Errorf("no repositories specified")
	}

	workspaceDir := "~/workspace"
	_, err := sshMgr.Execute(ctx, fmt.Sprintf("mkdir -p %s", workspaceDir))
	if err != nil {
		return "", fmt.Errorf("mkdir workspace: %w", err)
	}

	var primaryRepoDir string
	for i, repoURL := range repos {
		repoName := extractRepoName(repoURL)
		targetDir := fmt.Sprintf("%s/%s", workspaceDir, repoName)
		if i == 0 {
			primaryRepoDir = targetDir
		}

		cloneCmd := fmt.Sprintf("if [ ! -d %s ]; then git clone %q %s; fi", targetDir, repoURL, targetDir)
		if _, err := sshMgr.Execute(ctx, cloneCmd); err != nil {
			return "", fmt.Errorf("failed to clone repo %s: %w", repoURL, err)
		}

		checkoutCmd := fmt.Sprintf("cd %s && git fetch origin && (git checkout -b %s || git checkout %s)", targetDir, branch, branch)
		if _, err := sshMgr.Execute(ctx, checkoutCmd); err != nil {
			return "", fmt.Errorf("failed to checkout branch %s in %s: %w", branch, targetDir, err)
		}
	}

	return primaryRepoDir, nil
}

func (r *EC2LoopRunner) scaffoldTask(ctx context.Context, sshMgr domain.RemoteExecutor, repoDir string, spec domain.EC2LaunchSpec) error {
	taskContent := fmt.Sprintf(`# Autonomous Task Spec: %s

## Issue Key
%s

## Target Branch
%s

## Description
%s

## Instructions
1. Design, plan, and implement the requested feature/fix.
2. Verify all code changes by running tests.
3. Commit your changes with a clear message.
4. Push the branch %s to remote.
5. Create a GitHub Pull Request using 'gh pr create --fill --head %s'.
6. Save the resulting PR URL into ~/pr_url.txt.
`, spec.IssueKey, spec.IssueKey, spec.Branch, spec.TaskDescription, spec.Branch, spec.Branch)

	if err := sshMgr.WriteFile(ctx, filepath.Join(repoDir, ".aiman_task.md"), []byte(taskContent)); err != nil {
		return fmt.Errorf("write .aiman_task.md: %w", err)
	}

	prompt := fmt.Sprintf(`You are an autonomous coding agent executing a high-priority task.
Read .aiman_task.md for complete context.

TASK: %s

RULES:
1. Design and implement the required changes cleanly.
2. Run project tests and ensure the build passes.
3. Push the branch '%s' to remote origin.
4. Run 'gh pr create --fill --head %s' to open a Pull Request.
5. Write the Pull Request URL into ~/pr_url.txt once created.
6. When complete, exit cleanly.
`, spec.TaskDescription, spec.Branch, spec.Branch)

	if err := sshMgr.WriteFile(ctx, filepath.Join(repoDir, "AUTONOMOUS_PROMPT.txt"), []byte(prompt)); err != nil {
		return fmt.Errorf("write AUTONOMOUS_PROMPT.txt: %w", err)
	}

	watchdogScript := `#!/usr/bin/env bash
# Terminate the instance 10 minutes after the autonomous loop tmux session ends
while true; do
    if ! tmux has-session -t autonomous-loop 2>/dev/null; then
        echo "Session autonomous-loop not found. Initiating self-destruct in 5 minutes..."
        sudo shutdown -h +5
        exit 0
    fi
    sleep 60
done
`
	if err := sshMgr.WriteFile(ctx, "/home/ubuntu/watchdog.sh", []byte(watchdogScript)); err != nil {
		return fmt.Errorf("write watchdog.sh: %w", err)
	}
	_, _ = sshMgr.Execute(ctx, "chmod +x /home/ubuntu/watchdog.sh && nohup /home/ubuntu/watchdog.sh > /home/ubuntu/watchdog.log 2>&1 &")

	return nil
}

func (r *EC2LoopRunner) runAutonomousLoop(ctx context.Context, sshMgr domain.RemoteExecutor, repoDir string, spec domain.EC2LaunchSpec) (string, error) {
	var agentCmd string
	switch strings.ToLower(spec.AgentName) {
	case "claude", "claude-code":
		agentCmd = fmt.Sprintf("cd %s && claude --dangerously-skip-permissions -p \"$(cat AUTONOMOUS_PROMPT.txt)\"", repoDir)
	case "ageni":
		agentCmd = fmt.Sprintf("cd %s && ageni run \"$(cat AUTONOMOUS_PROMPT.txt)\"", repoDir)
	default:
		agentCmd = fmt.Sprintf("cd %s && claude --dangerously-skip-permissions -p \"$(cat AUTONOMOUS_PROMPT.txt)\"", repoDir)
	}

	execCmd := fmt.Sprintf("tmux new-session -d -s autonomous-loop %q", agentCmd)
	if _, err := sshMgr.Execute(ctx, execCmd); err != nil {
		return "", fmt.Errorf("start autonomous loop tmux session: %w", err)
	}

	timeout := time.Duration(spec.TimeoutMinutes) * time.Minute
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		prURLOut, err := sshMgr.Execute(ctx, "cat ~/pr_url.txt 2>/dev/null || true")
		if err == nil && strings.HasPrefix(strings.TrimSpace(prURLOut), "http") {
			return strings.TrimSpace(prURLOut), nil
		}

		// Also check if gh pr list returns a PR for this branch
		ghPROut, ghErr := sshMgr.Execute(ctx, fmt.Sprintf("cd %s && gh pr list --head %s --json url -q '.[0].url' 2>/dev/null || true", repoDir, spec.Branch))
		if ghErr == nil && strings.HasPrefix(strings.TrimSpace(ghPROut), "http") {
			return strings.TrimSpace(ghPROut), nil
		}

		// Check if tmux session exited
		sessions, err := sshMgr.ScanTmuxSessions(ctx)
		if err == nil {
			hasSession := false
			for _, s := range sessions {
				if s == "autonomous-loop" {
					hasSession = true
					break
				}
			}
			if !hasSession {
				prURLOut, _ := sshMgr.Execute(ctx, "cat ~/pr_url.txt 2>/dev/null || true")
				if strings.HasPrefix(strings.TrimSpace(prURLOut), "http") {
					return strings.TrimSpace(prURLOut), nil
				}
				ghPROut, _ := sshMgr.Execute(ctx, fmt.Sprintf("cd %s && gh pr list --head %s --json url -q '.[0].url' 2>/dev/null || true", repoDir, spec.Branch))
				if strings.HasPrefix(strings.TrimSpace(ghPROut), "http") {
					return strings.TrimSpace(ghPROut), nil
				}
				return "", fmt.Errorf("autonomous agent loop exited without producing a PR URL")
			}
		}

		time.Sleep(10 * time.Second)
	}

	return "", fmt.Errorf("autonomous loop timed out after %d minutes", spec.TimeoutMinutes)
}

func extractRepoName(repoURL string) string {
	u, err := url.Parse(repoURL)
	if err == nil && u.Path != "" {
		base := filepath.Base(u.Path)
		return strings.TrimSuffix(base, ".git")
	}
	parts := strings.Split(repoURL, "/")
	last := parts[len(parts)-1]
	return strings.TrimSuffix(last, ".git")
}
