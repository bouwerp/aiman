package contextstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/remotefile"
)

const (
	DirName          = "context"
	defaultListLimit = 50
	defaultPackLimit = 8
)

type fileMeta struct {
	ID         string `yaml:"id"`
	NS         string `yaml:"ns"`
	Key        string `yaml:"key"`
	Title      string `yaml:"title"`
	Abstract   string `yaml:"abstract"`
	Type       string `yaml:"type,omitempty"`
	Supersedes string `yaml:"supersedes,omitempty"`
	ValidUntil string `yaml:"valid_until,omitempty"`
	Author     string `yaml:"author,omitempty"`
	Verified   bool   `yaml:"verified,omitempty"`
	VerifiedAt string `yaml:"verified_at,omitempty"`
	Source     string `yaml:"source,omitempty"`
	SourceHash string `yaml:"source_hash,omitempty"`
	Confidence string `yaml:"confidence,omitempty"`
	Evidence   string `yaml:"evidence,omitempty"`
	Session    string `yaml:"session,omitempty"`
	Created    string `yaml:"created"`
}

// Files is a markdown directory store under root (typically ~/.aiman/context).
type Files struct {
	root     string
	mu       sync.Mutex
	ops      persistedStats
	ix       *Index
	ixBroken atomic.Bool
}

func NewFiles(root string) *Files {
	root = strings.TrimRight(root, "/\\")
	return &Files{root: root, ops: loadPersisted(root)}
}

// Root is ~/.aiman/context when aimanDir is the config directory.
func Root(aimanDir string) string {
	return path.Join(strings.TrimRight(aimanDir, "/\\"), DirName)
}

// AttachIndex makes this store use ix for reads, search and packs. The CLI and
// laptop leave it unattached and keep using the scan.
func (s *Files) AttachIndex(ix *Index) {
	s.ix = ix
	s.ixBroken.Store(false)
}

func (s *Files) indexOK() bool {
	return s.ix != nil && !s.ixBroken.Load()
}

func (s *Files) indexFailed(err error) {
	if s.ixBroken.CompareAndSwap(false, true) {
		log.Printf("context index unavailable: %v", err)
	}
}

// Reindex brings the index up to date from file mtimes. It reads only files
// newer than the stored high-water mark and drops ids whose files are gone.
func (s *Files) Reindex(ctx context.Context) (updated, removed int, err error) {
	if s.ix == nil {
		return 0, 0, nil
	}
	high, err := s.ix.HighWater(ctx)
	if err != nil {
		return 0, 0, err
	}
	paths, err := s.ix.Paths(ctx)
	if err != nil {
		return 0, 0, err
	}
	seen := make(map[string]bool, len(paths))
	maxMtime := high
	if _, statErr := os.Stat(s.root); statErr == nil {
		walkErr := fs.WalkDir(os.DirFS(s.root), ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".tmp") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			abs := path.Join(s.root, p)
			seen[abs] = true
			if info.ModTime().After(maxMtime) {
				maxMtime = info.ModTime()
			}
			if !info.ModTime().After(high) {
				return nil
			}
			raw, err := os.ReadFile(abs)
			if err != nil {
				return nil
			}
			e, err := decode(raw)
			if err != nil || e.ID == "" {
				return nil
			}
			if err := s.ix.Upsert(ctx, e, abs, info.ModTime()); err != nil {
				return err
			}
			updated++
			return nil
		})
		if walkErr != nil {
			return updated, removed, walkErr
		}
	} else if !os.IsNotExist(statErr) {
		return 0, 0, statErr
	}
	for id, p := range paths {
		if seen[p] {
			continue
		}
		if err := s.ix.Remove(ctx, id); err != nil {
			return updated, removed, err
		}
		removed++
	}
	if maxMtime.After(high) {
		if err := s.ix.SetHighWater(ctx, maxMtime); err != nil {
			return updated, removed, err
		}
	}
	return updated, removed, nil
}

func (s *Files) Put(ctx context.Context, e domain.ContextEntry) (stored domain.ContextEntry, err error) {
	start := time.Now()
	n := 0
	defer func() { s.observe("put", start, err, n) }()
	e, err = normalize(e)
	if err != nil {
		return domain.ContextEntry{}, err
	}
	p := absPath(s.root, e)
	if err = os.MkdirAll(path.Dir(p), 0o700); err != nil {
		return domain.ContextEntry{}, fmt.Errorf("creating context dir: %w", err)
	}
	body, err := encode(e)
	if err != nil {
		return domain.ContextEntry{}, err
	}
	n = len(body)
	tmp := p + ".tmp"
	if err = os.WriteFile(tmp, body, 0o600); err != nil {
		return domain.ContextEntry{}, fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err = os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return domain.ContextEntry{}, fmt.Errorf("renaming %s: %w", p, err)
	}
	if s.indexOK() {
		mtime := time.Now()
		if info, statErr := os.Stat(p); statErr == nil {
			mtime = info.ModTime()
		}
		if indexErr := s.ix.Upsert(ctx, e, p, mtime); indexErr != nil {
			s.indexFailed(indexErr)
		}
	}
	return e, nil
}

func (s *Files) Get(ctx context.Context, id string) (found *domain.ContextEntry, err error) {
	start := time.Now()
	n := 0
	defer func() { s.observe("get", start, err, n) }()
	id = SafeKey(id)
	if id == "" {
		return nil, fmt.Errorf("invalid id")
	}
	if s.indexOK() {
		p, pathErr := s.ix.PathOf(ctx, id)
		if pathErr == nil {
			raw, readErr := os.ReadFile(p)
			if readErr == nil {
				e, decodeErr := decode(raw)
				if decodeErr == nil && e.ID == id {
					n = len(e.Body)
					return &e, nil
				}
			}
		} else if !errors.Is(pathErr, sql.ErrNoRows) {
			s.indexFailed(pathErr)
		}
	}
	err = s.walk(func(e domain.ContextEntry) error {
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
	err = nil
	if found == nil {
		err = fmt.Errorf("context note not found")
		return nil, err
	}
	n = len(found.Body)
	return found, nil
}

func (s *Files) List(ctx context.Context, q domain.ContextQuery) ([]domain.ContextEntry, error) {
	return s.timedCollect(ctx, "list", q, false, false)
}

func (s *Files) Find(ctx context.Context, q domain.ContextQuery) ([]domain.ContextEntry, error) {
	return s.timedCollect(ctx, "find", q, true, true)
}

func (s *Files) timedCollect(ctx context.Context, op string, q domain.ContextQuery, text, live bool) (list []domain.ContextEntry, err error) {
	start := time.Now()
	defer func() { s.observe(op, start, err, len(list)) }()
	if live && text && s.indexOK() {
		if strings.TrimSpace(q.Text) == "" {
			return nil, nil
		}
		if list, err = s.ix.Search(ctx, q, true, time.Now()); err == nil {
			return list, nil
		}
		s.indexFailed(err)
	}
	return s.collect(q, text, live)
}

func (s *Files) Pack(ctx context.Context, q domain.ContextQuery) (text string, err error) {
	start := time.Now()
	defer func() { s.observe("pack", start, err, len(text)) }()
	if q.Limit <= 0 {
		q.Limit = defaultPackLimit
	}
	if s.indexOK() {
		entries, indexErr := s.ix.Search(ctx, q, true, time.Now())
		if indexErr == nil {
			return FormatPack(entries), nil
		}
		s.indexFailed(indexErr)
	}
	entries, err := s.collect(q, strings.TrimSpace(q.Text) != "", true)
	if err != nil {
		return "", err
	}
	return FormatPack(entries), nil
}

func (s *Files) Stats(_ context.Context) (domain.ContextStats, error) {
	notes, bytes, ns := s.measure()
	out := domain.ContextStats{
		Notes:       notes,
		Bytes:       bytes,
		Namespaces:  ns,
		Ops:         s.snapshotOps(),
		CollectedAt: time.Now().UTC(),
	}
	if out.Namespaces == nil {
		out.Namespaces = map[string]int{}
	}
	if out.Ops == nil {
		out.Ops = map[string]domain.ContextOpStat{}
	}
	return out, nil
}

func (s *Files) measure() (notes int, bytes int64, ns map[string]int) {
	ns = map[string]int{}
	if s.root == "" {
		return 0, 0, ns
	}
	if _, err := os.Stat(s.root); os.IsNotExist(err) {
		return 0, 0, ns
	}
	_ = s.walk(func(e domain.ContextEntry) error {
		notes++
		ns[e.Namespace]++
		return nil
	})
	_ = fs.WalkDir(os.DirFS(s.root), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		info, ierr := d.Info()
		if ierr == nil {
			bytes += info.Size()
		}
		return nil
	})
	return notes, bytes, ns
}

// PackSession builds the create/restart pack and records one pack op.
func (s *Files) PackSession(ctx context.Context, group, repo string) (text string) {
	start := time.Now()
	defer func() { s.observe("pack", start, nil, len(text)) }()
	if s.indexOK() {
		packed, err := s.packSessionIndexed(ctx, group, repo)
		if err == nil {
			return packed
		}
		s.indexFailed(err)
	}
	return packForSessionUnmetered(ctx, s, group, repo)
}

func (s *Files) packSessionIndexed(ctx context.Context, group, repo string) (string, error) {
	seen := make(map[string]bool)
	var all []domain.ContextEntry
	add := func(ns, key string) error {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil
		}
		list, err := s.ix.Search(ctx, domain.ContextQuery{
			Namespace: ns, Key: key, Limit: defaultPackLimit,
		}, true, time.Now())
		if err != nil {
			return err
		}
		for _, e := range list {
			if seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			all = append(all, e)
		}
		return nil
	}
	for _, target := range [][2]string{
		{domain.ContextNSGroup, group},
		{domain.ContextNSRepo, repo},
		{domain.ContextNSHost, "host"},
	} {
		if err := add(target[0], target[1]); err != nil {
			return "", err
		}
	}
	Rank(all)
	if len(all) > defaultPackLimit {
		all = all[:defaultPackLimit]
	}
	return FormatPack(all), nil
}

func (s *Files) collect(q domain.ContextQuery, requireText, live bool) ([]domain.ContextEntry, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	needle := strings.ToLower(strings.TrimSpace(q.Text))
	if requireText && needle == "" {
		return nil, nil
	}
	var all []domain.ContextEntry
	if err := s.walk(func(e domain.ContextEntry) error {
		all = append(all, e)
		return nil
	}); err != nil {
		return nil, err
	}
	var superseded map[string]bool
	if live {
		superseded = Superseded(all)
	}
	now := time.Now()
	var out []domain.ContextEntry
	for _, e := range all {
		if q.Namespace != "" && e.Namespace != q.Namespace {
			continue
		}
		if q.Key != "" && e.Key != q.Key && SafeKey(q.Key) != e.Key {
			continue
		}
		if needle != "" && !matchText(e, needle) {
			continue
		}
		if live && !Live(e, superseded, now) {
			continue
		}
		out = append(out, e)
	}
	Rank(out)
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
	typ, ok := domain.ParseContextType(string(e.Type))
	if !ok {
		return e, fmt.Errorf("invalid type %q", e.Type)
	}
	e.Type = typ
	switch e.Author {
	case "":
		e.Author = domain.ContextAuthorUnknown
	case domain.ContextAuthorAgent, domain.ContextAuthorHuman, domain.ContextAuthorAiman,
		domain.ContextAuthorVerifier, domain.ContextAuthorUnknown:
	default:
		return e, fmt.Errorf("invalid author %q", e.Author)
	}
	switch e.Confidence {
	case "", domain.ContextConfidenceLow, domain.ContextConfidenceMedium, domain.ContextConfidenceHigh:
	default:
		return e, fmt.Errorf("invalid confidence %q", e.Confidence)
	}
	e.Evidence = strings.TrimSpace(e.Evidence)
	rawSupersedes := strings.TrimSpace(e.Supersedes)
	e.Supersedes = SafeKey(e.Supersedes)
	if rawSupersedes != "" && e.Supersedes == "" {
		return e, fmt.Errorf("invalid supersedes %q", rawSupersedes)
	}
	if e.Type == domain.ContextTypeHabit && rawSupersedes != "" && e.Evidence == "" {
		return e, fmt.Errorf("superseding a habit requires --evidence")
	}
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
	validUntil := ""
	if !e.ValidUntil.IsZero() {
		validUntil = e.ValidUntil.UTC().Format(time.RFC3339)
	}
	verifiedAt := ""
	if !e.VerifiedAt.IsZero() {
		verifiedAt = e.VerifiedAt.UTC().Format(time.RFC3339)
	}
	meta := fileMeta{
		ID:         e.ID,
		NS:         e.Namespace,
		Key:        e.Key,
		Title:      e.Title,
		Abstract:   e.Abstract,
		Type:       string(e.Type),
		Supersedes: e.Supersedes,
		ValidUntil: validUntil,
		Author:     string(e.Author),
		Verified:   e.Verified,
		VerifiedAt: verifiedAt,
		Source:     e.Source,
		SourceHash: e.SourceHash,
		Confidence: string(e.Confidence),
		Evidence:   e.Evidence,
		Session:    e.SessionID,
		Created:    e.CreatedAt.UTC().Format(time.RFC3339),
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
	typ, ok := domain.ParseContextType(meta.Type)
	if !ok {
		typ = domain.ContextTypeFact
	}
	validUntil, _ := time.Parse(time.RFC3339, meta.ValidUntil)
	verifiedAt, _ := time.Parse(time.RFC3339, meta.VerifiedAt)
	author := domain.ContextAuthor(meta.Author)
	switch author {
	case domain.ContextAuthorAgent, domain.ContextAuthorHuman, domain.ContextAuthorAiman,
		domain.ContextAuthorVerifier, domain.ContextAuthorUnknown:
	default:
		author = domain.ContextAuthorUnknown
	}
	confidence := domain.ContextConfidence(meta.Confidence)
	switch confidence {
	case domain.ContextConfidenceLow, domain.ContextConfidenceMedium, domain.ContextConfidenceHigh:
	default:
		confidence = ""
	}
	return domain.ContextEntry{
		ID:         meta.ID,
		Namespace:  meta.NS,
		Key:        meta.Key,
		Title:      meta.Title,
		Abstract:   meta.Abstract,
		Body:       body,
		SessionID:  meta.Session,
		Type:       typ,
		Supersedes: SafeKey(meta.Supersedes),
		ValidUntil: validUntil,
		Author:     author,
		Verified:   meta.Verified,
		VerifiedAt: verifiedAt,
		Source:     meta.Source,
		SourceHash: meta.SourceHash,
		Confidence: confidence,
		Evidence:   meta.Evidence,
		CreatedAt:  created,
	}, nil
}

// PackForSession concatenates group then repo notes for task-file injection.
func PackForSession(ctx context.Context, store domain.ContextStore, group, repo string) string {
	if store == nil {
		return ""
	}
	if f, ok := store.(*Files); ok {
		return f.PackSession(ctx, group, repo)
	}
	return packFromLists(ctx, store, group, repo)
}

func packForSessionUnmetered(_ context.Context, s *Files, group, repo string) string {
	if s == nil {
		return ""
	}
	seen := map[string]bool{}
	var all []domain.ContextEntry
	add := func(ns, key string) {
		key = strings.TrimSpace(key)
		if key == "" {
			return
		}
		list, err := s.collect(domain.ContextQuery{Namespace: ns, Key: key, Limit: defaultPackLimit}, false, true)
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
	Rank(all)
	if len(all) > defaultPackLimit {
		all = all[:defaultPackLimit]
	}
	return FormatPack(all)
}

func packFromLists(ctx context.Context, store domain.ContextStore, group, repo string) string {
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
	superseded := Superseded(all)
	now := time.Now()
	live := all[:0]
	for _, e := range all {
		if Live(e, superseded, now) {
			live = append(live, e)
		}
	}
	all = live
	Rank(all)
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
	emitted := 0
	for _, e := range entries {
		if e.Type == domain.ContextTypeHabit && !e.Verified {
			continue // an unverified habit is a suggestion, not a rule
		}
		typ := e.Type
		if typ == "" {
			typ = domain.ContextTypeFact
		}
		fmt.Fprintf(&b, "- **%s** [%s] (`%s`): %s\n", e.Title, typ, e.ID, e.Abstract)
		emitted++
	}
	if emitted == 0 {
		return ""
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
		Type:      domain.ContextTypeSummary,
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
	Execute(ctx context.Context, cmd string) (string, error)
	WriteFile(ctx context.Context, path string, content []byte) error
}

// WriteSessionPack writes pack markdown into worktree/.aiman_context.md when
// it differs from the file already there.
func WriteSessionPack(ctx context.Context, remote remoteWriter, worktreePath, pack string) error {
	if remote == nil || strings.TrimSpace(pack) == "" {
		return nil
	}
	var b strings.Builder
	b.WriteString(domain.AimanScaffoldHeader)
	b.WriteString(pack)
	if !strings.HasSuffix(pack, "\n") {
		b.WriteByte('\n')
	}
	p := filepath.Join(worktreePath, domain.AimanContextFileName)
	_, err := remotefile.WriteIfChanged(ctx, remote, p, []byte(b.String()))
	return err
}
