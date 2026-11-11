// Package cigate is the local CI gate: a repository's list of CI commands run
// against a commit in a snapshot worktree on the host, a record of the result
// per session and commit, and the answers ci.gate gives to push and PR-create
// questions.
package cigate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ListPath is the repository file holding the team's gate list.
const ListPath = ".aiman/ci-local.yaml"

// DefaultTimeout bounds one command when the list names no timeout.
const DefaultTimeout = 45 * time.Minute

// Command is one entry of a gate list. Dir is relative to the repository root.
type Command struct {
	Name    string        `json:"name"`
	Run     string        `json:"run"`
	Dir     string        `json:"dir,omitempty"`
	Timeout time.Duration `json:"timeout"`
}

// List is a repository's gate: Setup runs first in the snapshot, then each
// command in order.
type List struct {
	Setup    string    `json:"setup,omitempty"`
	Commands []Command `json:"commands"`
}

type rawList struct {
	Setup    string       `yaml:"setup"`
	Commands []RawCommand `yaml:"commands"`
}

// RawCommand is a command as YAML spells it: the timeout is a duration string.
type RawCommand struct {
	Name    string `yaml:"name"`
	Run     string `yaml:"run"`
	Dir     string `yaml:"dir"`
	Timeout string `yaml:"timeout"`
}

// ParseList parses a gate list file and validates every command.
func ParseList(b []byte) (List, error) {
	var raw rawList
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return List{}, fmt.Errorf("parse %s: %w", ListPath, err)
	}
	return BuildList(raw.Setup, raw.Commands)
}

// BuildList validates raw commands and fills default timeouts. A list with no
// commands is an error: an empty gate would pass everything.
func BuildList(setup string, raw []RawCommand) (List, error) {
	if len(raw) == 0 {
		return List{}, errors.New("the gate list has no commands")
	}
	l := List{Setup: strings.TrimSpace(setup)}
	for i, rc := range raw {
		c, err := buildCommand(rc)
		if err != nil {
			return List{}, fmt.Errorf("command %d: %w", i+1, err)
		}
		l.Commands = append(l.Commands, c)
	}
	return l, nil
}

func buildCommand(rc RawCommand) (Command, error) {
	c := Command{Name: strings.TrimSpace(rc.Name), Run: strings.TrimSpace(rc.Run), Timeout: DefaultTimeout}
	if c.Name == "" {
		return c, errors.New("name is required")
	}
	if c.Run == "" {
		return c, fmt.Errorf("%s: run is required", c.Name)
	}
	if d := strings.TrimSpace(rc.Dir); d != "" {
		if err := ValidateDir(d); err != nil {
			return c, fmt.Errorf("%s: dir %q: %w", c.Name, d, err)
		}
		c.Dir = filepath.Clean(d)
	}
	if t := strings.TrimSpace(rc.Timeout); t != "" {
		d, err := time.ParseDuration(t)
		if err != nil || d <= 0 {
			return c, fmt.Errorf("%s: timeout %q is not a positive duration", c.Name, t)
		}
		c.Timeout = d
	}
	return c, nil
}

// ValidateDir rejects an absolute dir or one that escapes the repository.
func ValidateDir(d string) error {
	if filepath.IsAbs(d) {
		return errors.New("must be relative to the repository root")
	}
	clean := filepath.Clean(d)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("escapes the repository")
	}
	return nil
}

// LoadList returns override when it has commands; otherwise the list file as
// committed at commit in repoDir, never the working tree's copy. A commit
// without the file is no list and no error.
func LoadList(ctx context.Context, repoDir, commit string, override *List) (List, bool, error) {
	if override != nil && len(override.Commands) > 0 {
		return *override, true, nil
	}
	names, err := gitOut(ctx, repoDir, "ls-tree", "--name-only", commit, "--", ListPath)
	if err != nil {
		return List{}, false, err
	}
	if strings.TrimSpace(names) == "" {
		return List{}, false, nil
	}
	body, err := gitOut(ctx, repoDir, "show", commit+":"+ListPath)
	if err != nil {
		return List{}, false, err
	}
	l, err := ParseList([]byte(body))
	if err != nil {
		return List{}, false, err
	}
	return l, true, nil
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) //nolint:gosec // G204: fixed git subcommands; dir is a session's checkout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return string(out), nil
}
