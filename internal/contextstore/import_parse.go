package contextstore

import (
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

type memoryMeta struct {
	Name        string `yaml:"name"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Modified    string `yaml:"modified"`
	Metadata    struct {
		Modified string `yaml:"modified"`
	} `yaml:"metadata"`
}

func readMemoryMarkdown(path string) (title, abstract, body string, mtime int64, ok bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", "", "", 0, false
	}
	if info.Size() == 0 || info.Size() > maxImportBytes {
		return "", "", "", 0, false
	}
	raw, err := os.ReadFile(path)
	if err != nil || !utf8.Valid(raw) {
		return "", "", "", 0, false
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", "", "", 0, false
	}
	meta, rest := splitFrontmatter(text)
	title = firstNonEmpty(meta.Title, meta.Name, firstHeading(rest))
	abstract = firstNonEmpty(meta.Description, clipText(rest, abstractBudget))
	body = rest
	if body == "" {
		body = text
	}
	mtime = info.ModTime().Unix()
	if t := parseModified(firstNonEmpty(meta.Modified, meta.Metadata.Modified)); !t.IsZero() {
		mtime = t.Unix()
	}
	return title, abstract, body, mtime, true
}

func splitFrontmatter(s string) (memoryMeta, string) {
	if !strings.HasPrefix(s, "---\n") {
		return memoryMeta{}, s
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return memoryMeta{}, s
	}
	var meta memoryMeta
	_ = yaml.Unmarshal([]byte(rest[:end]), &meta)
	body := strings.TrimSpace(rest[end+4:])
	return meta, body
}

func firstHeading(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func clipText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n-3]) + "..."
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func parseModified(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func unixUTC(sec int64) time.Time {
	return time.Unix(sec, 0).UTC()
}

func gitOriginSlug(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		b, rerr := os.ReadFile(filepath.Join(dir, ".git"))
		if rerr != nil {
			return ""
		}
		line := strings.TrimSpace(string(b))
		const p = "gitdir:"
		if !strings.HasPrefix(strings.ToLower(line), p) {
			return ""
		}
		gitdir := strings.TrimSpace(line[len(p):])
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(dir, gitdir)
		}
		raw, err = os.ReadFile(filepath.Join(gitdir, "config"))
		if err != nil {
			raw, err = os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(gitdir)), "config"))
		}
		if err != nil {
			return ""
		}
	}
	return slugFromGitConfig(string(raw))
}

func slugFromGitConfig(cfg string) string {
	inOrigin := false
	for _, line := range strings.Split(cfg, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") {
			inOrigin = strings.EqualFold(trim, `[remote "origin"]`)
			continue
		}
		if !inOrigin || !strings.HasPrefix(trim, "url") {
			continue
		}
		_, url, ok := strings.Cut(trim, "=")
		if !ok {
			continue
		}
		if slug := slugFromRemoteURL(strings.TrimSpace(url)); slug != "" {
			return slug
		}
	}
	return ""
}

func slugFromRemoteURL(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimSuffix(u, ".git")
	u = strings.TrimPrefix(u, "ssh://git@")
	u = strings.TrimPrefix(u, "git@")
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	if i := strings.Index(u, ":"); i >= 0 && !strings.Contains(u[:i], "/") {
		u = u[i+1:]
	} else if i := strings.Index(u, "/"); i >= 0 {
		u = u[i+1:]
	}
	u = strings.Trim(u, "/")
	parts := strings.Split(u, "/")
	if len(parts) < 2 {
		return ""
	}
	owner := parts[len(parts)-2]
	repo := parts[len(parts)-1]
	if owner == "" || repo == "" {
		return ""
	}
	return owner + "/" + repo
}

func decodeClaudeProject(name string) string {
	if !strings.HasPrefix(name, "-") {
		return name
	}
	return strings.ReplaceAll(name, "-", "/")
}

func repoFromPath(dir string) string {
	if slug := gitOriginSlug(dir); slug != "" {
		return slug
	}
	base := filepath.Base(dir)
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return ""
}
