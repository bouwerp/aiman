package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	DirName      = ".aiman"
	ConfigName   = "config.yaml"
	DBName       = "aiman.db"
	LogName      = "aiman.log"
	DebugLogName = "debug.log"
)

type Config struct {
	Integrations  Integrations             `yaml:"integrations"`
	Git           GitConfig                `yaml:"git,omitempty"`
	Features      FeatureFlags             `yaml:"features,omitempty"`
	Skills        SkillsConfig             `yaml:"skills,omitempty"`
	AI            AIConfig                 `yaml:"ai,omitempty"`
	AWS           AWSDefaults              `yaml:"aws,omitempty"`
	AgentDefaults map[string]AgentDefaults `yaml:"agent_defaults,omitempty"`
	Sync          SyncConfig               `yaml:"sync,omitempty"`
	Remotes       []Remote                 `yaml:"remotes"`
	ActiveRemote  string                   `yaml:"active_remote"`

	// PermissionsTightened records that Load had to remove group/other access
	// from the config file, meaning the API token in it had been readable by
	// other users. Not persisted.
	PermissionsTightened bool `yaml:"-"`
	// PermissionsError records why the repair failed, if it did. Not persisted.
	PermissionsError error `yaml:"-"`
}

// ServeConfig is the subset of settings used by aiman serve on a remote host.
// It excludes remotes because they describe the machine that runs the TUI.
type ServeConfig struct {
	Integrations  Integrations             `yaml:"integrations"`
	Git           GitConfig                `yaml:"git,omitempty"`
	Skills        SkillsConfig             `yaml:"skills,omitempty"`
	AgentDefaults map[string]AgentDefaults `yaml:"agent_defaults,omitempty"`
}

// MarshalServeConfig serializes the settings a remote Agent API needs.
func (c *Config) MarshalServeConfig() ([]byte, error) {
	if c == nil {
		return yaml.Marshal(ServeConfig{})
	}
	return yaml.Marshal(ServeConfig{
		Integrations:  c.Integrations,
		Git:           c.Git,
		Skills:        c.Skills,
		AgentDefaults: c.AgentDefaults,
	})
}

// AWSDefaults sets what the AWS fields on the session summary screen start
// with, so a profile does not have to be retyped for every session.
type AWSDefaults struct {
	// DefaultProfile is the local ~/.aws profile new sessions use to mint
	// credentials. Empty means fall back to the delegation's source_profile.
	DefaultProfile string `yaml:"default_profile,omitempty"`
	// DefaultRegion is the region new sessions start with. Empty falls back to
	// the delegation's region.
	DefaultRegion string `yaml:"default_region,omitempty"`
	// IncludeProfiles is the local ~/.aws profile names aiman will use. nil
	// (omitted) means every local profile; an empty list means none.
	IncludeProfiles *[]string `yaml:"include_profiles,omitempty"`
}

// AgentDefaults is the launch model and reasoning effort for one agent binary
// (keyed by command: claude, grok, agy, codex, …).
type AgentDefaults struct {
	Model  string `yaml:"model,omitempty"`
	Effort string `yaml:"effort,omitempty"`
}

// ResolveAWSSessionDefaults returns the profile and region a new session on
// this remote should be pre-filled with.
//
// Precedence, most specific first: the remote's own aws_default_profile /
// aws_default_region, then the global aws: block, then the delegation's own
// source_profile / region. The user can still override either on the summary
// screen; this only decides what that screen starts with.
func (c *Config) ResolveAWSSessionDefaults(remote Remote, delegation *AWSDelegation) (profile, region string) {
	if delegation != nil {
		profile, region = delegation.SourceProfile, delegation.Region
	}
	if c != nil {
		if v := strings.TrimSpace(c.AWS.DefaultProfile); v != "" {
			profile = v
		}
		if v := strings.TrimSpace(c.AWS.DefaultRegion); v != "" {
			region = v
		}
	}
	if v := strings.TrimSpace(remote.AWSDefaultProfile); v != "" {
		profile = v
	}
	if v := strings.TrimSpace(remote.AWSDefaultRegion); v != "" {
		region = v
	}
	return profile, region
}

// SyncConfig tunes what mutagen mirrors between the remote worktree and the
// local copy. The defaults exclude dependency and build directories, which
// dominate transfer time on a slow link without being worth mirroring.
type SyncConfig struct {
	// Ignore adds patterns on top of the built-in set. An entry prefixed with
	// "!" removes the matching built-in instead, e.g. "!dist" to mirror a
	// project that genuinely tracks its dist directory.
	Ignore []string `yaml:"ignore,omitempty"`
	// UseDefaultIgnores mirrors everything when explicitly false. Unset means
	// the built-in ignore set applies.
	UseDefaultIgnores *bool `yaml:"use_default_ignores,omitempty"`
}

// SyncIgnoresEnabled reports whether the built-in ignore set should apply.
func (c *Config) SyncIgnoresEnabled() bool {
	if c == nil || c.Sync.UseDefaultIgnores == nil {
		return true
	}
	return *c.Sync.UseDefaultIgnores
}

// SyncIgnorePatterns returns the user's additional ignore patterns.
func (c *Config) SyncIgnorePatterns() []string {
	if c == nil {
		return nil
	}
	return c.Sync.Ignore
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
	// ClassifyModel is the model used for session activity classification.
	// Defaults to Model. Smaller models were measured and rejected: against
	// panes captured from real sessions, qwen3:4b scored 3/3 at ~600 ms while
	// qwen3:1.7b managed 1/3 at ~210 ms. Set this only if a faster model proves
	// itself on your own sessions.
	ClassifyModel string `yaml:"classify_model,omitempty"`
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
	// Root is the parent of main clones and `<repo>@<branch>` worktrees on this
	// host. serve uses it when remotes[] is empty (the Agent API config has no
	// remotes). Empty means infer: $HOME/repos if that directory exists, else $HOME.
	Root string `yaml:"root,omitempty"`
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
	// AutoUpdateRemotes keeps each remote's agent API on the client's release.
	// A pointer so an absent key means enabled: almost every runtime fix lives
	// in serve rather than the TUI, and a remote silently left behind loses them
	// while looking healthy.
	AutoUpdateRemotes *bool `yaml:"auto_update_remotes,omitempty"`
}

// AutoUpdateRemotes reports whether the client should update remotes that are
// running an older release than itself. Defaults to on.
func (c *Config) AutoUpdateRemotes() bool {
	if c == nil || c.Features.AutoUpdateRemotes == nil {
		return true
	}
	return *c.Features.AutoUpdateRemotes
}

type JiraConfig struct {
	URL              string `yaml:"url"`
	Email            string `yaml:"email"`
	APIToken         string `yaml:"api_token"`
	TransitionStatus string `yaml:"transition_status,omitempty"` // Status to move issue to when starting (e.g. "In Development")
	// IssueStatuses limits the issue picker to issues in these statuses. Empty falls back
	// to jira.DefaultIssueStatuses.
	IssueStatuses []string `yaml:"issue_statuses,omitempty"`
}

type Remote struct {
	Name string `yaml:"name"`
	Host string `yaml:"host"`
	User string `yaml:"user"`
	Root string `yaml:"root"`
	// AWSDelegation describes a named profile (assume-role) to merge into ~/.aws/config on this remote.
	// No secrets are stored here; source_profile must already exist on the server.
	AWSDelegation *AWSDelegation `yaml:"aws_delegation,omitempty"`
	// AWSDelegations allows multiple delegation profiles per remote (e.g. default + prod).
	// When both AWSDelegation and AWSDelegations are set, all entries are used.
	AWSDelegations []*AWSDelegation `yaml:"aws_delegations,omitempty"`
	// AWSDefaultProfile overrides the global aws.default_profile for sessions
	// created on this remote. Empty means inherit.
	AWSDefaultProfile string `yaml:"aws_default_profile,omitempty"`
	// AWSDefaultRegion overrides the global aws.default_region for this remote.
	AWSDefaultRegion string `yaml:"aws_default_region,omitempty"`
	// SessionBackend selects how new sessions host their terminal on this
	// remote: "pty" (default) or "tmux". Empty inherits the product default (pty).
	SessionBackend string `yaml:"session_backend,omitempty"`
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
	// ManagedRole, when true, tells aiman to automatically create the IAM role
	// (account_id + role_name) if it does not already exist, with a trust policy
	// that allows the source_profile identity to assume it. The role is given a
	// passthrough policy (Allow *:* on *) so effective permissions equal the
	// intersection with the source_profile user's own permissions.
	// TODO: add fine-grained permission configuration once the core flow is proven.
	ManagedRole bool `yaml:"managed_role,omitempty"`
	// Optional restrictions applied when SyncCredentials is true.
	Region          string   `yaml:"region,omitempty"`           // written into the remote profile as "region = <value>"
	Regions         []string `yaml:"regions,omitempty"`          // restrict credentials via aws:RequestedRegion condition policy; default ["us-east-2"] in UI
	SessionPolicy   string   `yaml:"session_policy,omitempty"`   // inline JSON IAM policy passed to sts assume-role --policy
	DurationSeconds int      `yaml:"duration_seconds,omitempty"` // credential lifetime 900–43200; 0 = DefaultDurationSeconds (43200)
}

// AllDelegations returns all AWSDelegation entries for the remote, combining
// both the singular AWSDelegation field and the AWSDelegations slice.
// Entries with SyncCredentials=false are included; callers filter as needed.
func (r Remote) AllDelegations() []*AWSDelegation {
	var out []*AWSDelegation
	if r.AWSDelegation != nil {
		out = append(out, r.AWSDelegation)
	}
	out = append(out, r.AWSDelegations...)
	return out
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

	// Save writes 0600, but nothing kept it that way: a manual edit, a restore
	// from backup, or an older release could leave a plaintext API token in a
	// world-readable file. Repair it on every load and tell the caller, so the
	// exposure is both fixed and reported rather than silently carried forward.
	tightened, permErr := SecureConfigFile(path)
	cfg.PermissionsTightened = tightened
	cfg.PermissionsError = permErr

	return &cfg, nil
}

// configFileMode is the only permission set appropriate for a file holding a
// plaintext API token: readable and writable by its owner alone.
const configFileMode os.FileMode = 0600

// SecureConfigFile removes group and other access from the config file. It
// reports whether it had to change anything, so a caller can surface the fact
// that the token had been exposed. A missing file is not an error.
func SecureConfigFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	// Compare only the permission bits; setuid and friends are not our concern.
	current := info.Mode().Perm()
	if current&0077 == 0 {
		return false, nil
	}
	if err := os.Chmod(path, configFileMode); err != nil {
		return false, fmt.Errorf("config file %s is mode %#o and could not be tightened to %#o: %w", path, current, configFileMode, err)
	}
	return true, nil
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

	// The directory holding config.yaml may not exist yet (first run, or a
	// caller that saves before EnsureDir has ever been called) — create it
	// rather than silently failing the write. 0700: this directory also
	// holds the session database and SSH control sockets.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// GetDir returns ~/.aiman.
func GetDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, DirName), nil
}

// GetDBPath returns the path to the database file.
func GetDBPath() (string, error) {
	dir, err := GetDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, DBName), nil
}

func GetServeLogPath() (string, error) {
	dir, err := GetDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "serve.log"), nil
}

// GetDebugLogPath is the default file for `aiman --debug`.
func GetDebugLogPath() (string, error) {
	dir, err := GetDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, DebugLogName), nil
}

// GetLogPath returns the path of the log file the TUI redirects to, so
// background goroutines never write onto the rendered frame.
func GetLogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, DirName, LogName), nil
}

// EnsureDir ensures that the configuration directory exists.
func EnsureDir() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}
	dir := filepath.Join(home, DirName)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		// 0700: this directory holds config.yaml with a plaintext API token,
		// the session database, and the SSH control sockets.
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	return nil
}
