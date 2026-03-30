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
	Remotes      []Remote     `yaml:"remotes"`
	ActiveRemote string       `yaml:"active_remote"`
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
	URL      string `yaml:"url"`
	Email    string `yaml:"email"`
	APIToken string `yaml:"api_token"`
}

type Remote struct {
	Name string `yaml:"name"`
	Host string `yaml:"host"`
	User string `yaml:"user"`
	Root string `yaml:"root"`
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
