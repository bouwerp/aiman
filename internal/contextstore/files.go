package contextstore

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	DirName          = "context"
	defaultListLimit = 50
	defaultPackLimit = 8
)

type fileMeta struct {
	ID       string `yaml:"id"`
	NS       string `yaml:"ns"`
	Key      string `yaml:"key"`
	Title    string `yaml:"title"`
	Abstract string `yaml:"abstract"`
	Session  string `yaml:"session,omitempty"`
	Created  string `yaml:"created"`
}

// Files is a markdown directory store under root (typically ~/.aiman/context).
type Files struct {
	root string
}

func NewFiles(root string) *Files {
	return &Files{root: strings.TrimRight(root, "/\\")}
}

// Root is ~/.aiman/context when aimanDir is the config directory.
func Root(aimanDir string) string {
	return path.Join(strings.TrimRight(aimanDir, "/\\"), DirName)
}

func (s *Files) Put(_ context.Context, e domain.ContextEntry) (domain.ContextEntry, error) {
	e, err := normalize(e)
	if err != nil {
		return domain.ContextEntry{}, err
	}
	p := absPath(s.root, e)
	if err := os.MkdirAll(path.Dir(p), 0o700); err != nil {
		return domain.ContextEntry{}, fmt.Errorf("creating context dir: %w", err)
	}
	body, err := encode(e)
	if err != nil {
		return domain.ContextEntry{}, err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return domain.ContextEntry{}, fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return domain.ContextEntry{}, fmt.Errorf("renaming %s: %w", p, err)
	}
	return e, nil
}

func (s *Files) Get(_ context.Context, id string) (*domain.ContextEntry, error) {
	id = SafeKey(id)
	if id == "" {
		return nil, fmt.Errorf("invalid id")
	}
	var found *domain.ContextEntry
	err := s.walk(func(e domain.ContextEntry) error {
		if e.ID == id {
			cp := e
			found = &cp
			return errStop
		}
		return nil
	})
	if err != nil && err != errStop {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("context note not found")
	}
	return found, nil
}

func (s *Files) List(_ context.Context, q domain.ContextQuery) ([]domain.ContextEntry, error) {
	return s.collect(q, false)
}

func (s *Files) Find(_ context.Context, q domain.ContextQuery) ([]domain.ContextEntry, error) {
	return s.collect(q, true)
}

func (s *Files) Pack(ctx context.Context, q domain.ContextQuery) (string, error) {
	if q.Limit <= 0 {
		q.Limit = defaultPackLimit
	}
	var entries []domain.ContextEntry
	var err error
	if strings.TrimSpace(q.Text) != "" {
		entries, err = s.Find(ctx, q)
	} else {
		entries, err = s.List(ctx, q)
	}
	if err != nil {
		return "", err
	}
	return FormatPack(entries), nil
}

func (s *Files) collect(q domain.ContextQuery, requireText bool) ([]domain.ContextEntry, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	needle := strings.ToLower(strings.TrimSpace(q.Text))
	if requireText && needle == "" {
		return nil, nil
	}
	var out []domain.ContextEntry
	err := s.walk(func(e domain.ContextEntry) error {
		if q.Namespace != "" && e.Namespace != q.Namespace {
			return nil
		}
		if q.Key != "" && e.Key != q.Key && SafeKey(q.Key) != e.Key {
			return nil
		}
		if needle != "" && !matchText(e, needle) {
			return nil
		}
		out = append(out, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

var errStop = fmt.Errorf("stop")

func (s *Files) walk(fn func(domain.ContextEntry) error) error {
	if s.root == "" {
		return fmt.Errorf("context root is empty")
	}
	if _, err := os.Stat(s.root); os.IsNotExist(err) {
		return nil
	}
	err := fs.WalkDir(os.DirFS(s.root), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".tmp") {
			return nil
		}
		raw, rerr := os.ReadFile(path.Join(s.root, p))
		if rerr != nil {
			return nil
		}
		e, perr := decode(raw)
		if perr != nil || e.ID == "" {
			return nil
		}
		return fn(e)
	})
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func matchText(e domain.ContextEntry, needle string) bool {
	blob := strings.ToLower(e.Title + "\n" + e.Abstract + "\n" + e.Body)
	return strings.Contains(blob, needle)
}

func normalize(e domain.ContextEntry) (domain.ContextEntry, error) {
	e.Namespace = strings.ToLower(strings.TrimSpace(e.Namespace))
	if e.Namespace == "" {
		e.Namespace = domain.ContextNSHost
	}
	switch e.Namespace {
	case domain.ContextNSGroup, domain.ContextNSRepo, domain.ContextNSHost:
	default:
		return e, fmt.Errorf("invalid namespace %q", e.Namespace)
	}
	rawKey := strings.TrimSpace(e.Key)
	e.Key = SafeKey(e.Key)
	if e.Key == "" {
		if rawKey == "" {
			e.Key = "host"
		} else {
			return e, fmt.Errorf("invalid key")
		}
	}
	e.Title = strings.TrimSpace(e.Title)
	if e.Title == "" {
		return e, fmt.Errorf("title is required")
	}
	e.Abstract = strings.TrimSpace(e.Abstract)
	if e.Abstract == "" {
		e.Abstract = e.Title
	}
	e.Body = strings.TrimSpace(e.Body)
	e.SessionID = strings.TrimSpace(e.SessionID)
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	e.ID = SafeKey(e.ID)
	if e.ID == "" {
		return e, fmt.Errorf("invalid id")
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	return e, nil
}

// SafeKey is a single path element: letters, digits, - _ .
func SafeKey(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "/", "__")
	s = strings.ReplaceAll(s, "\\", "__")
	if s == "" || len(s) > 80 || strings.Contains(s, "..") {
		return ""
	}
	for _, r := range s {
		if r > unicode.MaxASCII {
			return ""
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.' {
			return ""
		}
	}
	return s
}

func absPath(root string, e domain.ContextEntry) string {
	ns := e.Namespace
	switch ns {
	case domain.ContextNSGroup:
		ns = "groups"
	case domain.ContextNSRepo:
		ns = "repos"
	}
	return path.Join(root, ns, e.Key, e.ID+".md")
}

func encode(e domain.ContextEntry) ([]byte, error) {
	meta := fileMeta{
		ID:       e.ID,
		NS:       e.Namespace,
		Key:      e.Key,
		Title:    e.Title,
		Abstract: e.Abstract,
		Session:  e.SessionID,
		Created:  e.CreatedAt.UTC().Format(time.RFC3339),
	}
	y, err := yaml.Marshal(meta)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(y)
	b.WriteString("---\n\n")
	b.WriteString(e.Body)
	if e.Body != "" && !strings.HasSuffix(e.Body, "\n") {
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

func decode(raw []byte) (domain.ContextEntry, error) {
	s := string(raw)
	if !strings.HasPrefix(s, "---\n") {
		return domain.ContextEntry{}, fmt.Errorf("missing frontmatter")
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return domain.ContextEntry{}, fmt.Errorf("unterminated frontmatter")
	}
	var meta fileMeta
	if err := yaml.Unmarshal([]byte(rest[:end]), &meta); err != nil {
		return domain.ContextEntry{}, err
	}
	body := strings.TrimSpace(rest[end+4:])
	created, _ := time.Parse(time.RFC3339, meta.Created)
	return domain.ContextEntry{
		ID:        meta.ID,
		Namespace: meta.NS,
		Key:       meta.Key,
		Title:     meta.Title,
		Abstract:  meta.Abstract,
		Body:      body,
		SessionID: meta.Session,
		CreatedAt: created,
	}, nil
}

// PackForSession concatenates group then repo notes for task-file injection.
func PackForSession(ctx context.Context, store domain.ContextStore, group, repo string) string {
	if store == nil {
		return ""
	}
	seen := map[string]bool{}
	var all []domain.ContextEntry
	add := func(ns, key string) {
		key = strings.TrimSpace(key)
		if key == "" {
			return
		}
		list, err := store.List(ctx, domain.ContextQuery{Namespace: ns, Key: key, Limit: defaultPackLimit})
		if err != nil {
			return
		}
		for _, e := range list {
			if seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			all = append(all, e)
		}
	}
	add(domain.ContextNSGroup, group)
	add(domain.ContextNSRepo, repo)
	add(domain.ContextNSHost, "host")
	if len(all) > defaultPackLimit {
		all = all[:defaultPackLimit]
	}
	return FormatPack(all)
}

// FormatPack renders abstracts for injection into a session task file.
func FormatPack(entries []domain.ContextEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Shared context\n\n")
	b.WriteString("Notes from earlier sessions on this host. Use `aiman context get ID` for the full note.\n\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "- **%s** (`%s`): %s\n", e.Title, e.ID, e.Abstract)
	}
	return strings.TrimSpace(b.String()) + "\n"
}

// EncodeFile returns the absolute path and markdown for a remote WriteFile.
func EncodeFile(root string, e domain.ContextEntry) (string, []byte, error) {
	e, err := normalize(e)
	if err != nil {
		return "", nil, err
	}
	body, err := encode(e)
	if err != nil {
		return "", nil, err
	}
	return absPath(strings.TrimRight(root, "/\\"), e), body, nil
}

// EntryFromSnapshot builds one note from an archive snapshot.
func EntryFromSnapshot(snap *domain.SessionSnapshot, group, sessionID string) domain.ContextEntry {
	e := domain.ContextEntry{
		Title:     strings.TrimSpace(snap.Summary),
		SessionID: sessionID,
		CreatedAt: snap.CreatedAt,
	}
	if e.Title == "" {
		e.Title = "Session snapshot"
	}
	e.Abstract = e.Title
	var body strings.Builder
	for _, line := range snap.Overview {
		fmt.Fprintf(&body, "- %s\n", line)
	}
	if len(snap.NextSteps) > 0 {
		body.WriteString("\nNext steps:\n")
		for _, line := range snap.NextSteps {
			fmt.Fprintf(&body, "- %s\n", line)
		}
	}
	e.Body = strings.TrimSpace(body.String())
	group = strings.TrimSpace(group)
	if group == "" {
		group = strings.TrimSpace(snap.IssueKey)
	}
	repo := strings.TrimSpace(snap.RepoName)
	switch {
	case group != "":
		e.Namespace = domain.ContextNSGroup
		e.Key = group
	case repo != "":
		e.Namespace = domain.ContextNSRepo
		e.Key = repo
	default:
		e.Namespace = domain.ContextNSHost
		e.Key = "host"
	}
	return e
}

type remoteWriter interface {
	WriteFile(ctx context.Context, path string, content []byte) error
}

// WriteSessionPack writes pack markdown into worktree/.aiman_context.md.
func WriteSessionPack(ctx context.Context, remote remoteWriter, worktreePath, pack string) error {
	if remote == nil || strings.TrimSpace(pack) == "" {
		return nil
	}
	var b strings.Builder
	b.WriteString("<!--\n")
	b.WriteString("DO NOT COMMIT — session scaffolding generated by Aiman.\n")
	b.WriteString("-->\n\n")
	b.WriteString("> **Do not commit this file.** It is generated for this session only and is listed in `.gitignore`.\n\n")
	b.WriteString(pack)
	if !strings.HasSuffix(pack, "\n") {
		b.WriteByte('\n')
	}
	p := filepath.Join(worktreePath, domain.AimanContextFileName)
	return remote.WriteFile(ctx, p, []byte(b.String()))
}
