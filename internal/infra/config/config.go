package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	DirName    = ".aiman"
	ConfigName = "config.yaml"
	DBName     = "aiman.db"
)

type Config struct {
	Integrations Integrations `yaml:"integrations"`
	Git          GitConfig    `yaml:"git,omitempty"`
	Features     FeatureFlags `yaml:"features,omitempty"`
	Skills       SkillsConfig `yaml:"skills,omitempty"`
	AI           AIConfig     `yaml:"ai,omitempty"`
	Remotes      []Remote     `yaml:"remotes"`
	ActiveRemote string       `yaml:"active_remote"`
}

// AIConfig controls the local SLM intelligence features powered by Ollama.
type AIConfig struct {
	// Enabled turns on AI-powered features. When false, all intelligence calls
	// return ErrIntelligenceUnavailable without contacting Ollama.
	Enabled bool `yaml:"enabled"`
	// OllamaHost is the base URL of the Ollama server. Defaults to http://localhost:11434.
	OllamaHost string `yaml:"ollama_host,omitempty"`
	// Model is the Ollama model name to use. Defaults to qwen3:4b.
	Model string `yaml:"model,omitempty"`
}

type SkillsConfig struct {
	Repo string `yaml:"repo"`
	Path string `yaml:"path,omitempty"`
}

type Integrations struct {
	Jira JiraConfig `yaml:"jira"`
}

type GitConfig struct {
	// IncludePersonal, when nil, means true (include your GitHub account repos).
	// Omitted keys in YAML must not disable personal repos; use explicit false to opt out.
	IncludePersonal *bool    `yaml:"include_personal,omitempty"`
	IncludeOrgs     []string `yaml:"include_orgs,omitempty"`
	IncludePatterns []string `yaml:"include_patterns,omitempty"`
	ExcludePatterns []string `yaml:"exclude_patterns,omitempty"`
}

// PersonalReposEnabled returns whether repos under the authenticated GitHub user should be listed.
func PersonalReposEnabled(g *GitConfig) bool {
	if g == nil || g.IncludePersonal == nil {
		return true
	}
	return *g.IncludePersonal
}

type FeatureFlags struct {
	InputPromptDetection bool `yaml:"input_prompt_detection,omitempty"`
}

type JiraConfig struct {
	URL              string `yaml:"url"`
	Email            string `yaml:"email"`
	APIToken         string `yaml:"api_token"`
	TransitionStatus string `yaml:"transition_status,omitempty"` // Status to move issue to when starting (e.g. "In Development")
}

type Remote struct {
	Name string `yaml:"name"`
	Host string `yaml:"host"`
	User string `yaml:"user"`
	Root string `yaml:"root"`
	// AWSDelegation describes a named profile (assume-role) to merge into ~/.aws/config on this remote.
	// No secrets are stored here; source_profile must already exist on the server.
	AWSDelegation *AWSDelegation `yaml:"aws_delegation,omitempty"`
}

// AWSDelegation is stored in aiman config; role_arn on the remote is derived from
// account_id + role_name. account_id is resolved via local `aws sts get-caller-identity`
// for source_profile when saving; Profile defaults to "default" in the TUI.
//
//	[default]
//	role_arn = arn:aws:iam::ACCOUNT:role/RoleName   (generated)
//	source_profile = their-long-lived-profile
//	region = us-east-1                              (optional)
type AWSDelegation struct {
	Profile         string `yaml:"profile,omitempty"`          // defaults to "default" in UI
	AccountID       string `yaml:"account_id,omitempty"`       // from local AWS CLI
	RoleName        string `yaml:"role_name,omitempty"`        // empty → TemporaryDelegatedRole in generated ARN
	SourceProfile   string `yaml:"source_profile,omitempty"`   // local profile used for account lookup; must exist on remote
	SyncCredentials bool   `yaml:"sync_credentials,omitempty"` // whether to push temporary session tokens to the remote
	// Optional restrictions applied when SyncCredentials is true.
	Region          string   `yaml:"region,omitempty"`           // written into the remote profile as "region = <value>"
	Regions         []string `yaml:"regions,omitempty"`          // restrict credentials via aws:RequestedRegion condition policy; default ["us-east-2"] in UI
	SessionPolicy   string   `yaml:"session_policy,omitempty"`   // inline JSON IAM policy passed to sts assume-role --policy
	DurationSeconds int      `yaml:"duration_seconds,omitempty"` // credential lifetime 900–43200; 0 = AWS default
}

// UniqueRemotes returns remotes with duplicate SSH targets (same host, user, root) removed.
// The first entry in the config order is kept. Prevents scanning the same machine twice,
// which duplicated sessions and mutagen handling.
func UniqueRemotes(remotes []Remote) []Remote {
	if len(remotes) <= 1 {
		return remotes
	}
	seen := make(map[string]bool, len(remotes))
	out := make([]Remote, 0, len(remotes))
	for _, r := range remotes {
		key := strings.TrimSpace(r.Host) + "\x00" + strings.TrimSpace(r.User) + "\x00" + strings.TrimSpace(r.Root)
		if r.Host == "" {
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// GetConfigPath returns the path to the configuration file.
func GetConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, DirName, ConfigName), nil
}

// Load loads the configuration from the config file.
func Load() (*Config, error) {
	path, err := GetConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file not found at %s. Please run 'aiman init' (to be implemented)", path)
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return &cfg, nil
}

// Save saves the configuration to the config file.
func (c *Config) Save() error {
	path, err := GetConfigPath()
	if err != nil {
		return err
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// GetDBPath returns the path to the database file.
func GetDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, DirName, DBName), nil
}

// EnsureDir ensures that the configuration directory exists.
func EnsureDir() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}
	dir := filepath.Join(home, DirName)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	return nil
}
