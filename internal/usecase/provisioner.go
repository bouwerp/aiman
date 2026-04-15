package usecase

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

type Provisioner struct {
	remoteExecutor domain.RemoteExecutor
}

func NewProvisioner(remoteExecutor domain.RemoteExecutor) *Provisioner {
	return &Provisioner{
		remoteExecutor: remoteExecutor,
	}
}

func (p *Provisioner) GetSteps() []domain.ProvisionStep {
	return []domain.ProvisionStep{
		{
			ID:      "base-tools",
			Name:    "Install Base Tools",
			Command: "if command -v tmux >/dev/null 2>&1 && command -v git >/dev/null 2>&1 && command -v curl >/dev/null 2>&1 && command -v wget >/dev/null 2>&1; then echo 'Base tools already installed'; else sudo apt-get update && sudo apt-get install -y tmux git curl wget; fi",
		},
		{
			ID:      "nodejs",
			Name:    "Install Node.js",
			Command: "if command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1; then echo 'Node.js already installed'; else curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash - && sudo apt-get install -y nodejs; fi",
		},
		{
			ID:      "claude-code",
			Name:    "Install Claude Code",
			Command: "if command -v claude >/dev/null 2>&1 || command -v claude-code >/dev/null 2>&1; then echo 'Claude Code already installed'; else npm install -g @anthropic-ai/claude-code || (mkdir -p ~/.npm-global && npm config set prefix ~/.npm-global && npm install -g @anthropic-ai/claude-code); fi",
		},
		{
			ID:      "gh-cli",
			Name:    "Install GitHub CLI",
			Command: "if command -v gh >/dev/null 2>&1; then echo 'GitHub CLI already installed'; else (type -p wget >/dev/null || (sudo apt update && sudo apt-get install wget -y)) && sudo mkdir -p -m 755 /etc/apt/keyrings && wget -qO- https://cli.github.com/packages/githubcli-archive-keyring.gpg | sudo tee /etc/apt/keyrings/githubcli-archive-keyring.gpg > /dev/null && sudo chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg && echo \"deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main\" | sudo tee /etc/apt/sources.list.d/github-cli.list > /dev/null && sudo apt update && sudo apt install gh -y; fi",
		},
		{
			ID:      "skills-framework",
			Name:    "Install Agent Skills",
			Command: "mkdir -p ~/.aiman/skills && if [ ! -d ~/.aiman/skills/agent-skills ]; then git clone https://github.com/realfi-co/agent-skills.git ~/.aiman/skills/agent-skills; else git -C ~/.aiman/skills/agent-skills pull; fi",
		},
	}
}

func (p *Provisioner) GetStepsWithLocalSSHKey() ([]domain.ProvisionStep, error) {
	pubKey, err := findLocalPublicKey()
	if err != nil {
		return nil, err
	}

	steps := []domain.ProvisionStep{
		{
			ID:      "local-ssh-key",
			Name:    "Authorize Local SSH Public Key",
			Command: fmt.Sprintf("mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && (grep -qxF %q ~/.ssh/authorized_keys || printf '%%s\\n' %q >> ~/.ssh/authorized_keys)", pubKey, pubKey),
		},
	}
	steps = append(steps, p.GetSteps()...)
	return steps, nil
}

func (p *Provisioner) Provision(ctx context.Context, progress chan<- domain.ProvisionProgress) error {
	steps, err := p.GetStepsWithLocalSSHKey()
	if err != nil {
		return err
	}
	return p.remoteExecutor.ProvisionRemote(ctx, steps, progress)
}

func findLocalPublicKey() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory for SSH key lookup: %w", err)
	}

	candidates := []string{
		filepath.Join(home, ".ssh", "id_ed25519.pub"),
		filepath.Join(home, ".ssh", "id_rsa.pub"),
		filepath.Join(home, ".ssh", "id_ecdsa.pub"),
		filepath.Join(home, ".ssh", "id_dsa.pub"),
	}

	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		key := strings.TrimSpace(string(b))
		if key != "" {
			return key, nil
		}
	}

	return "", fmt.Errorf("no local SSH public key found (looked for id_ed25519.pub, id_rsa.pub, id_ecdsa.pub, id_dsa.pub)")
}
