package aimanskill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	ActionInstalled = "installed"
	ActionUpdated   = "updated"
	ActionCurrent   = "current"
)

// FileResult is one skill path after EnsureFile.
type FileResult struct {
	Path   string
	Action string
}

func skillRel() string {
	return filepath.Join("skills", "aiman", "SKILL.md")
}

// UserSkillFiles are host-level skill paths agents scan under $HOME.
func UserSkillFiles(home string) []string {
	home = strings.TrimRight(home, "/\\")
	if home == "" {
		return nil
	}
	rel := skillRel()
	return []string{
		filepath.Join(home, ".claude", rel),
		filepath.Join(home, ".agents", rel),
		filepath.Join(home, ".cursor", rel),
		filepath.Join(home, ".codex", rel),
		filepath.Join(home, ".grok", rel),
		filepath.Join(home, ".gemini", rel),
		filepath.Join(home, ".config", "kilo", rel),
		filepath.Join(home, ".config", "muse", rel),
		filepath.Join(home, ".kilo", rel),
		filepath.Join(home, ".deepcode", rel),
	}
}

// ProjectSkillFiles are worktree-local skill paths. Slash-joined so a laptop
// can write them onto a Unix remote.
func ProjectSkillFiles(root string) []string {
	root = strings.TrimRight(root, "/\\")
	if root == "" {
		return nil
	}
	return []string{
		root + "/.agents/skills/aiman/SKILL.md",
		root + "/.claude/skills/aiman/SKILL.md",
		root + "/.cursor/skills/aiman/SKILL.md",
		root + "/.codex/skills/aiman/SKILL.md",
		root + "/.grok/skills/aiman/SKILL.md",
		root + "/.gemini/skills/aiman/SKILL.md",
		root + "/.kilo/skills/aiman/SKILL.md",
		root + "/.deepcode/skills/aiman/SKILL.md",
	}
}

// BundleFile is one file of the skill, installed in every aiman skill dir.
type BundleFile struct {
	Name    string
	Content string
}

// Bundle is the skill's file set: SKILL.md and the design page template.
func Bundle() []BundleFile {
	return []BundleFile{{Name: "SKILL.md", Content: Text}, {Name: TemplateName, Content: Template}}
}

// Sibling replaces the file name of a skill path, keeping its separator, so
// it works on host paths and on slash-joined remote paths alike.
func Sibling(skillPath, name string) string {
	return skillPath[:strings.LastIndexAny(skillPath, "/\\")+1] + name
}

// EnsureFile writes the embedded skill when the path is missing or stale.
func EnsureFile(path string) (FileResult, error) {
	return ensureContent(path, Text)
}

// ensureBundle ensures every bundle file beside the skill path.
func ensureBundle(skillPath string) ([]FileResult, error) {
	var out []FileResult
	var first error
	for _, f := range Bundle() {
		res, err := ensureContent(Sibling(skillPath, f.Name), f.Content)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		out = append(out, res)
	}
	return out, first
}

func ensureContent(path, content string) (FileResult, error) {
	res := FileResult{Path: path}
	if path == "" || content == "" {
		return res, nil
	}
	existing, err := os.ReadFile(path)
	switch {
	case err == nil && string(existing) == content:
		res.Action = ActionCurrent
		return res, nil
	case err == nil:
		res.Action = ActionUpdated
	case os.IsNotExist(err):
		res.Action = ActionInstalled
	default:
		return res, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return res, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return res, fmt.Errorf("writing %s: %w", path, err)
	}
	return res, nil
}

// EnsureOnHost installs or updates the skill's file set in user-level agent
// dirs and in each existing project root. Missing project roots are skipped.
func EnsureOnHost(home string, projectRoots []string) ([]FileResult, error) {
	var out []FileResult
	var first error
	ensure := func(skillPath string) {
		res, err := ensureBundle(skillPath)
		out = append(out, res...)
		if err != nil && first == nil {
			first = err
		}
	}
	for _, p := range UserSkillFiles(home) {
		ensure(p)
	}
	seen := map[string]bool{}
	for _, root := range projectRoots {
		root = strings.TrimRight(root, "/\\")
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		st, err := os.Stat(root)
		if err != nil || !st.IsDir() {
			continue
		}
		for _, p := range ProjectSkillFiles(root) {
			ensure(p)
		}
	}
	return out, first
}

// Summarize is a one-line serve log of ensure results.
func Summarize(results []FileResult) string {
	nInst, nUpd, nCur := 0, 0, 0
	for _, r := range results {
		switch r.Action {
		case ActionInstalled:
			nInst++
		case ActionUpdated:
			nUpd++
		default:
			nCur++
		}
	}
	return fmt.Sprintf("installed %d, updated %d, current %d", nInst, nUpd, nCur)
}
