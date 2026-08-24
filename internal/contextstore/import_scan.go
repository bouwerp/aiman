package contextstore

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func collectClaude(home string) []MemoryFile {
	root := envOr("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	projects := filepath.Join(root, "projects")
	entries, err := os.ReadDir(projects)
	if err != nil {
		return nil
	}
	var out []MemoryFile
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		memDir := filepath.Join(projects, ent.Name(), "memory")
		mds, err := os.ReadDir(memDir)
		if err != nil {
			continue
		}
		repo := repoFromPath(decodeClaudeProject(ent.Name()))
		var names []string
		for _, f := range mds {
			if f.IsDir() || !strings.HasSuffix(strings.ToLower(f.Name()), ".md") {
				continue
			}
			names = append(names, f.Name())
		}
		skipIndex := false
		for _, n := range names {
			if !strings.EqualFold(n, "MEMORY.md") {
				skipIndex = true
				break
			}
		}
		for _, n := range names {
			if skipIndex && strings.EqualFold(n, "MEMORY.md") {
				continue
			}
			p := filepath.Join(memDir, n)
			title, abstract, body, mtime, ok := readMemoryMarkdown(p)
			if !ok {
				continue
			}
			out = append(out, MemoryFile{
				Agent:    "claude",
				RelPath:  relFrom(home, p),
				Title:    title,
				Abstract: abstract,
				Body:     body,
				Repo:     repo,
				mtime:    mtime,
			})
		}
	}
	return capNewest(out, defaultImportLimit)
}

func collectGrok(home string) []MemoryFile {
	root := filepath.Join(envOr("GROK_HOME", filepath.Join(home, ".grok")), "memory")
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil
	}
	var out []MemoryFile
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "sessions" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(d.Name(), "MEMORY.md") {
			return nil
		}
		title, abstract, body, mtime, ok := readMemoryMarkdown(p)
		if !ok {
			return nil
		}
		relDir := filepath.Dir(p)
		repo := ""
		if filepath.Clean(relDir) != filepath.Clean(root) {
			repo = grokWorkspaceRepo(filepath.Base(relDir), relDir)
		}
		if title == "" {
			if repo != "" {
				title = "workspace memory"
			} else {
				title = "global memory"
			}
		}
		out = append(out, MemoryFile{
			Agent:    "grok",
			RelPath:  relFrom(home, p),
			Title:    title,
			Abstract: abstract,
			Body:     body,
			Repo:     repo,
			mtime:    mtime,
		})
		return nil
	})
	return capNewest(out, defaultImportLimit)
}

func grokWorkspaceRepo(name, dir string) string {
	if slug := gitOriginSlug(dir); slug != "" {
		return slug
	}
	base := name
	if i := strings.LastIndex(base, "-"); i > 0 && len(base)-i == 9 {
		hex := base[i+1:]
		if isHex(hex) {
			base = base[:i]
		}
	}
	base = strings.ReplaceAll(base, "-", "/")
	if strings.Count(base, "/") == 1 {
		return base
	}
	return ""
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}

func collectCodex(home string) []MemoryFile {
	root := envOr("CODEX_HOME", filepath.Join(home, ".codex"))
	mem := filepath.Join(root, "memories")
	entries, err := os.ReadDir(mem)
	if err != nil {
		return nil
	}
	var out []MemoryFile
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := strings.ToLower(ent.Name())
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		if name == "raw_memories.md" {
			continue
		}
		p := filepath.Join(mem, ent.Name())
		title, abstract, body, mtime, ok := readMemoryMarkdown(p)
		if !ok {
			continue
		}
		if title == "" {
			title = strings.TrimSuffix(ent.Name(), filepath.Ext(ent.Name()))
		}
		out = append(out, MemoryFile{
			Agent:    "codex",
			RelPath:  relFrom(home, p),
			Title:    title,
			Abstract: abstract,
			Body:     body,
			mtime:    mtime,
		})
	}
	return capNewest(out, defaultImportLimit)
}

func collectAgy(home string) []MemoryFile {
	gemini := filepath.Join(home, ".gemini")
	roots := []string{
		envOr("ANTIGRAVITY_CLI_ROOT", filepath.Join(gemini, "antigravity-cli")),
		filepath.Join(gemini, "antigravity"),
		filepath.Join(gemini, "antigravity-ide"),
	}
	var out []MemoryFile
	seen := map[string]bool{}
	for _, root := range roots {
		brain := filepath.Join(root, "brain")
		entries, err := os.ReadDir(brain)
		if err != nil {
			continue
		}
		for _, ent := range entries {
			if !ent.IsDir() {
				continue
			}
			p := filepath.Join(brain, ent.Name(), "walkthrough.md")
			if seen[p] {
				continue
			}
			seen[p] = true
			title, abstract, body, mtime, ok := readMemoryMarkdown(p)
			if !ok {
				continue
			}
			if title == "" {
				title = "walkthrough"
			}
			out = append(out, MemoryFile{
				Agent:    "agy",
				RelPath:  relFrom(home, p),
				Title:    title,
				Abstract: abstract,
				Body:     body,
				Repo:     repoFromWalkthrough(body),
				mtime:    mtime,
			})
		}
	}
	return capNewest(out, defaultImportLimit)
}

func repoFromWalkthrough(body string) string {
	const mark = "file://"
	i := strings.Index(body, mark)
	if i < 0 {
		return ""
	}
	rest := body[i+len(mark):]
	end := strings.IndexAny(rest, ") \n\t\"'")
	if end < 0 {
		end = len(rest)
	}
	p := rest[:end]
	dir := p
	if !strings.HasSuffix(p, "/") {
		dir = filepath.Dir(p)
	}
	for n := 0; n < 8 && dir != "" && dir != "." && dir != string(filepath.Separator); n++ {
		if slug := gitOriginSlug(dir); slug != "" {
			return slug
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func capNewest(files []MemoryFile, limit int) []MemoryFile {
	if limit <= 0 || len(files) <= limit {
		return files
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].mtime != files[j].mtime {
			return files[i].mtime > files[j].mtime
		}
		return files[i].RelPath > files[j].RelPath
	})
	return files[:limit]
}
