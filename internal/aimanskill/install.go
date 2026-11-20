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

// AimanSkillName is the aiman skill's directory name.
const AimanSkillName = "aiman"

// userAgentDirs are the agent dirs under $HOME, relative, holding skills/.
var userAgentDirs = [][]string{
	{".claude"},
	{".agents"},
	{".cursor"},
	{".codex"},
	{".grok"},
	{".gemini"},
	{".config", "kilo"},
	{".config", "muse"},
	{".kilo"},
	{".deepcode"},
}

// projectAgentDirs are the agent dirs in a worktree that hold skills/.
var projectAgentDirs = []string{".agents", ".claude", ".cursor", ".codex", ".grok", ".gemini", ".kilo", ".deepcode"}

// UserSkillDirs are the host-level directories of one skill under $HOME.
func UserSkillDirs(home, skill string) []string {
	home = strings.TrimRight(home, "/\\")
	if home == "" || skill == "" {
		return nil
	}
	out := make([]string, 0, len(userAgentDirs))
	for _, d := range userAgentDirs {
		parts := append([]string{home}, d...)
		parts = append(parts, "skills", skill)
		out = append(out, filepath.Join(parts...))
	}
	return out
}

// ProjectSkillDirs are the worktree-local directories of one skill.
// Slash-joined so a laptop can write them onto a Unix remote.
func ProjectSkillDirs(root, skill string) []string {
	root = strings.TrimRight(root, "/\\")
	if root == "" || skill == "" {
		return nil
	}
	out := make([]string, 0, len(projectAgentDirs))
	for _, d := range projectAgentDirs {
		out = append(out, root+"/"+d+"/skills/"+skill)
	}
	return out
}

// UserSkillFiles are the aiman skill's SKILL.md paths agents scan under $HOME.
func UserSkillFiles(home string) []string {
	dirs := UserSkillDirs(home, AimanSkillName)
	for i, d := range dirs {
		dirs[i] = filepath.Join(d, "SKILL.md")
	}
	return dirs
}

// ProjectSkillFiles are the aiman skill's worktree-local SKILL.md paths,
// slash-joined.
func ProjectSkillFiles(root string) []string {
	dirs := ProjectSkillDirs(root, AimanSkillName)
	for i, d := range dirs {
		dirs[i] = d + "/SKILL.md"
	}
	return dirs
}

// IsProjectSkillPath reports whether a worktree-relative path is a file of a
// bundled skill in one of the project agent dirs.
func IsProjectSkillPath(rel string) bool {
	rel = "/" + strings.TrimLeft(filepath.ToSlash(rel), "/")
	for _, s := range Skills() {
		for _, dir := range ProjectSkillDirs("root", s.Name) {
			dir = strings.TrimPrefix(dir, "root")
			for _, f := range s.Files {
				if strings.HasSuffix(rel, dir+"/"+f.Name) {
					return true
				}
			}
		}
	}
	return false
}

// Skill is one bundled skill: its directory name and the files in it.
type Skill struct {
	Name  string
	Files []BundleFile
}

// Skills lists every bundled skill, in install order.
func Skills() []Skill {
	return []Skill{
		{Name: AimanSkillName, Files: Bundle()},
		{Name: HarnessSkillName, Files: []BundleFile{{Name: "SKILL.md", Content: HarnessSkillText}, {Name: HarnessName, Content: Harness}}},
	}
}

// BundleFile is one file of the skill, installed in every aiman skill dir.
type BundleFile struct {
	Name    string
	Content string
}

// Bundle is the skill's file set: SKILL.md, the design page template and
// the harness reference.
func Bundle() []BundleFile {
	return []BundleFile{{Name: "SKILL.md", Content: Text}, {Name: TemplateName, Content: Template}, {Name: HarnessName, Content: Harness}}
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

// ensureSkill ensures every file of a skill in its directory.
func ensureSkill(dir string, files []BundleFile) ([]FileResult, error) {
	var out []FileResult
	var first error
	for _, f := range files {
		res, err := ensureContent(filepath.Join(dir, f.Name), f.Content)
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

// EnsureOnHost installs or updates every bundled skill in user-level agent
// dirs and in each existing project root. Missing project roots are skipped.
func EnsureOnHost(home string, projectRoots []string) ([]FileResult, error) {
	var out []FileResult
	var first error
	ensure := func(dir string, files []BundleFile) {
		res, err := ensureSkill(dir, files)
		out = append(out, res...)
		if err != nil && first == nil {
			first = err
		}
	}
	skills := Skills()
	for _, s := range skills {
		for _, d := range UserSkillDirs(home, s.Name) {
			ensure(d, s.Files)
		}
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
		for _, s := range skills {
			for _, d := range ProjectSkillDirs(root, s.Name) {
				ensure(d, s.Files)
			}
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
