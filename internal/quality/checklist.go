// Package quality parses domain checklists, renders and parses the kit
// blocks of .aiman_task.md, and builds the remote shell scripts that list,
// match and push checklists. It runs nothing itself: callers send the
// scripts through a RemoteExecutor, one bounded Execute each.
package quality

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	// RepoChecklistDir holds repository checklists, relative to the worktree.
	RepoChecklistDir = ".aiman/checklists"
	// UserChecklistDir holds user checklists, relative to $HOME.
	UserChecklistDir = ".aiman/checklists"
	// MaxChecklists caps how many checklists one session is pointed at.
	MaxChecklists = 8

	maxChecklistBytes = 16 << 10
)

// Checklist sources.
const (
	SourceRepo = "repo"
	SourceUser = "user"
)

// globList accepts applies_to as a single string or a list of strings.
type globList []string

func (g *globList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*g = globList{n.Value}
		return nil
	case yaml.SequenceNode:
		var list []string
		if err := n.Decode(&list); err != nil {
			return err
		}
		*g = list
		return nil
	default:
		return fmt.Errorf("applies_to must be a string or a list, line %d", n.Line)
	}
}

type frontmatter struct {
	AppliesTo globList `yaml:"applies_to"`
}

// ParseChecklist reads the YAML frontmatter between leading "---" lines.
// A file without frontmatter, or without applies_to, applies to every
// repository session. Unterminated or invalid frontmatter is an error.
func ParseChecklist(name, path, source string, content []byte) (domain.Checklist, error) {
	c := domain.Checklist{Name: name, Path: path, Source: source}
	fm, ok, err := splitFrontmatter(content)
	if err != nil || !ok {
		return c, err
	}
	var meta frontmatter
	if err := yaml.Unmarshal(fm, &meta); err != nil {
		return c, fmt.Errorf("checklist %s: frontmatter: %w", path, err)
	}
	if len(meta.AppliesTo) > 0 {
		c.AppliesTo = []string(meta.AppliesTo)
	}
	return c, nil
}

func splitFrontmatter(content []byte) ([]byte, bool, error) {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	first, rest, _ := bytes.Cut(content, []byte("\n"))
	if string(bytes.TrimRight(first, "\r")) != "---" {
		return nil, false, nil
	}
	var fm []byte
	for len(rest) > 0 {
		var line []byte
		line, rest, _ = bytes.Cut(rest, []byte("\n"))
		if string(bytes.TrimRight(line, "\r")) == "---" {
			return fm, true, nil
		}
		fm = append(append(fm, line...), '\n')
	}
	return nil, false, errors.New("checklist frontmatter has no closing ---")
}

// PathspecsFor turns checklist globs into git pathspecs. Git's :(glob)
// anchors at the repository root, so a glob without a slash also gets a
// **/ form to match at any depth, as in .gitignore.
func PathspecsFor(globs []string) []string {
	var out []string
	for _, g := range globs {
		out = append(out, ":(glob)"+g)
		if !strings.Contains(g, "/") {
			out = append(out, ":(glob)**/"+g)
		}
	}
	return out
}

// Select keeps checklists without globs and those whose name matched. A
// repo checklist shadows a user checklist of the same name. The result is
// ordered repo first, then by name, and capped at MaxChecklists.
func Select(all []domain.Checklist, matched map[string]bool) []domain.Checklist {
	repoNames := map[string]bool{}
	for _, c := range all {
		if c.Source == SourceRepo {
			repoNames[c.Name] = true
		}
	}
	var out []domain.Checklist
	for _, c := range all {
		if c.Source != SourceRepo && repoNames[c.Name] {
			continue
		}
		if len(c.AppliesTo) > 0 && !matched[c.Name] {
			continue
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := out[i].Source == SourceRepo, out[j].Source == SourceRepo
		if ri != rj {
			return ri
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > MaxChecklists {
		out = out[:MaxChecklists]
	}
	return out
}
