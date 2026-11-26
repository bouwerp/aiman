package artifact

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	MaxFiles     = 255
	MaxFileBytes = 25 << 20
	MaxTotal     = 64 << 20
)

// File is one file of a version.
type File struct {
	Path        string // published path, relative, slash separated
	Data        []byte
	ContentType string // empty: from the extension
}

// ShareList names who besides the owners may view an artifact. Entries are
// lower case.
type ShareList struct {
	Emails  []string `json:"emails"`
	Domains []string `json:"domains"`
}

// Meta is a/<id>/meta.json, the only object the Worker reads for access.
type Meta struct {
	Latest  int       `json:"latest"`
	Title   string    `json:"title"`
	Share   ShareList `json:"share"`
	Deleted bool      `json:"deleted,omitempty"`
}

// Publisher writes versions to the bucket and records them in the store.
type Publisher struct {
	Bucket Bucket
	Store  *SQLStore
	Host   string // "aiman.fyi"; URL is https://<Host>/a/<id>/
	Kit    []File // copied into every version under kit/
	Now    func() time.Time
	NewID  func() string
	// CanWrite reports whether caller may add a version to an artifact
	// owned by owner. Nil: only the owner.
	CanWrite func(owner, caller string) bool
}

// PublishRequest is one publish: a new artifact, or a version of ID.
type PublishRequest struct {
	ID            string // empty: new artifact
	Title         string
	Session       string // caller's session id; "" is the operator
	ExpectVersion int    // 0: no check
	Files         []File // must hold "index.html"
}

// Published is what a publish made.
type Published struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	URL     string `json:"url"`
}

var idRe = regexp.MustCompile(`^[a-z2-7]{20}$`)

// ValidID reports whether id has the artifact id form.
func ValidID(id string) bool { return idRe.MatchString(id) }

// NewID returns 20 characters of lower-case base32 from 12 random bytes.
func NewID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

// URL is the address of an artifact's latest version.
func (p *Publisher) URL(id string) string {
	return "https://" + p.Host + "/a/" + id + "/"
}

func (p *Publisher) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Publisher) newID() string {
	if p.NewID != nil {
		return p.NewID()
	}
	return NewID()
}

func metaKey(id string) string { return "a/" + id + "/meta.json" }

func versionPrefix(id string, n int) string {
	return "a/" + id + "/v/" + strconv.Itoa(n) + "/"
}

// Publish writes the version files with If-None-Match, then meta.json with
// If-Match (If-None-Match for a new artifact), then the store. A failure
// after a file PUT leaves orphan files that meta.json never points at.
func (p *Publisher) Publish(ctx context.Context, r PublishRequest) (Published, error) {
	files, err := p.versionFiles(r.Files)
	if err != nil {
		return Published{}, err
	}
	var (
		rec   Record
		meta  Meta
		etag  string
		isNew = r.ID == ""
	)
	if isNew {
		rec, err = p.freshRecord(ctx, r)
		if err != nil {
			return Published{}, err
		}
		meta = Meta{Title: rec.Title, Share: ShareList{}.normal()}
	} else {
		if !ValidID(r.ID) {
			return Published{}, fmt.Errorf("%w: id %q is not 20 lower-case base32 characters", ErrInvalid, r.ID)
		}
		rec, err = p.liveRecord(ctx, r.ID)
		if err != nil {
			return Published{}, err
		}
		if !p.mayWrite(rec.Session, r.Session) {
			return Published{}, fmt.Errorf("%w: artifact %s belongs to session %s", ErrNotOwner, rec.ID, rec.Session)
		}
		meta, etag, err = p.readMeta(ctx, rec)
		if err != nil {
			return Published{}, err
		}
		if meta.Deleted {
			return Published{}, fmt.Errorf("%w: %s", ErrNotFound, rec.ID)
		}
		if r.Title != "" {
			meta.Title = r.Title
		}
	}
	latest := max(meta.Latest, rec.Latest)
	if r.ExpectVersion != 0 && r.ExpectVersion != latest {
		return Published{}, fmt.Errorf("%w: artifact %s is at version %d, not %d", ErrConflict, rec.ID, latest, r.ExpectVersion)
	}
	n := latest + 1
	prefix := versionPrefix(rec.ID, n)
	infos := make([]FileInfo, 0, len(files))
	for _, f := range files {
		ct := contentType(f)
		if _, err := p.Bucket.Put(ctx, prefix+f.Path, f.Data, ct, Cond{IfNoneMatch: true}); err != nil {
			if errors.Is(err, ErrPrecondition) {
				return Published{}, fmt.Errorf("%w: version %d of %s is already written", ErrConflict, n, rec.ID)
			}
			return Published{}, err
		}
		sum := sha256.Sum256(f.Data)
		infos = append(infos, FileInfo{Path: f.Path, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(f.Data)), Type: ct})
	}
	meta.Latest = n
	if err := p.putMeta(ctx, rec.ID, meta, etag); err != nil {
		return Published{}, err
	}
	rec.Latest = n
	rec.Title = meta.Title
	rec.Share = meta.Share.normal()
	if err := p.Store.Save(ctx, rec); err != nil {
		return Published{}, fmt.Errorf("artifact %s v%d is live but not recorded: %w", rec.ID, n, err)
	}
	if err := p.Store.AddVersion(ctx, rec.ID, Version{N: n, Session: r.Session, Created: p.now().UTC(), Files: infos}); err != nil {
		return Published{}, fmt.Errorf("artifact %s v%d is live but not recorded: %w", rec.ID, n, err)
	}
	return Published{ID: rec.ID, Version: n, URL: p.URL(rec.ID)}, nil
}

// freshRecord picks an id no record and no meta.json holds yet.
func (p *Publisher) freshRecord(ctx context.Context, r PublishRequest) (Record, error) {
	for range 4 {
		id := p.newID()
		if !ValidID(id) {
			return Record{}, fmt.Errorf("artifact: generated id %q is not valid", id)
		}
		if _, err := p.Store.Get(ctx, id); !errors.Is(err, ErrNotFound) {
			if err != nil {
				return Record{}, err
			}
			continue
		}
		if _, _, err := p.Bucket.Get(ctx, metaKey(id)); !errors.Is(err, ErrNotFound) {
			if err != nil {
				return Record{}, err
			}
			continue
		}
		return Record{ID: id, Title: r.Title, Session: r.Session, Created: p.now().UTC()}, nil
	}
	return Record{}, errors.New("artifact: no free id after 4 tries")
}

// liveRecord reads a record that is not deleted.
func (p *Publisher) liveRecord(ctx context.Context, id string) (Record, error) {
	rec, err := p.Store.Get(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if rec.Deleted {
		return Record{}, fmt.Errorf("%w: %s was deleted", ErrNotFound, id)
	}
	return rec, nil
}

func (p *Publisher) mayWrite(owner, caller string) bool {
	if caller == "" || caller == owner {
		return true
	}
	return p.CanWrite != nil && p.CanWrite(owner, caller)
}

// readMeta reads meta.json and its ETag. A record whose meta.json is missing
// gets one built from the record, written later with If-None-Match (etag "").
func (p *Publisher) readMeta(ctx context.Context, rec Record) (Meta, string, error) {
	data, etag, err := p.Bucket.Get(ctx, metaKey(rec.ID))
	if errors.Is(err, ErrNotFound) {
		return Meta{Latest: rec.Latest, Title: rec.Title, Share: rec.Share.normal()}, "", nil
	}
	if err != nil {
		return Meta{}, "", err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, "", fmt.Errorf("artifact %s meta.json: %w", rec.ID, err)
	}
	m.Share = m.Share.normal()
	return m, etag, nil
}

// putMeta writes meta.json with If-Match etag, or If-None-Match when etag is
// empty. A failed condition is ErrConflict.
func (p *Publisher) putMeta(ctx context.Context, id string, m Meta, etag string) error {
	m.Share = m.Share.normal()
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c := Cond{IfMatch: etag, IfNoneMatch: etag == ""}
	if _, err := p.Bucket.Put(ctx, metaKey(id), data, "application/json", c); err != nil {
		if errors.Is(err, ErrPrecondition) {
			return fmt.Errorf("%w: meta.json of %s changed under this write", ErrConflict, id)
		}
		return err
	}
	return nil
}

// versionFiles checks the caller's files and adds the kit under kit/.
func (p *Publisher) versionFiles(in []File) ([]File, error) {
	if len(in) > MaxFiles {
		return nil, fmt.Errorf("%w: %d files, at most %d", ErrLimits, len(in), MaxFiles)
	}
	seen := make(map[string]bool, len(in)+len(p.Kit))
	var total int64
	out := make([]File, 0, len(in)+len(p.Kit))
	for _, f := range in {
		if err := checkPath(f.Path); err != nil {
			return nil, err
		}
		if strings.HasPrefix(f.Path, "kit/") && len(p.Kit) > 0 {
			return nil, fmt.Errorf("%w: path %q is reserved for the theme kit", ErrInvalid, f.Path)
		}
		if seen[f.Path] {
			return nil, fmt.Errorf("%w: path %q given twice", ErrInvalid, f.Path)
		}
		seen[f.Path] = true
		if len(f.Data) > MaxFileBytes {
			return nil, fmt.Errorf("%w: %s is %d bytes, at most %d", ErrLimits, f.Path, len(f.Data), MaxFileBytes)
		}
		total += int64(len(f.Data))
		if total > MaxTotal {
			return nil, fmt.Errorf("%w: files total over %d bytes", ErrLimits, MaxTotal)
		}
		out = append(out, f)
	}
	if !seen["index.html"] {
		return nil, fmt.Errorf("%w: index.html is required", ErrInvalid)
	}
	for _, k := range p.Kit {
		if err := checkPath(k.Path); err != nil {
			return nil, fmt.Errorf("kit: %w", err)
		}
		out = append(out, File{Path: "kit/" + k.Path, Data: k.Data, ContentType: k.ContentType})
	}
	return out, nil
}

// checkPath accepts a relative, slash separated path with no empty, "." or
// ".." element, no backslash and no control character.
func checkPath(p string) error {
	bad := func(why string) error { return fmt.Errorf("%w: path %q %s", ErrInvalid, p, why) }
	switch {
	case p == "":
		return bad("is empty")
	case len(p) > 512:
		return bad("is over 512 bytes")
	case strings.HasPrefix(p, "/"):
		return bad("is absolute")
	case strings.ContainsRune(p, '\\'):
		return bad("holds a backslash")
	case strings.ContainsFunc(p, unicode.IsControl):
		return bad("holds a control character")
	}
	for _, el := range strings.Split(p, "/") {
		if el == "" || el == "." || el == ".." {
			return bad("has an empty, . or .. element")
		}
	}
	return nil
}

var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".htm":   "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".json":  "application/json",
	".map":   "application/json",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".avif":  "image/avif",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".txt":   "text/plain; charset=utf-8",
	".md":    "text/markdown; charset=utf-8",
	".csv":   "text/csv; charset=utf-8",
	".pdf":   "application/pdf",
	".wasm":  "application/wasm",
	".mp4":   "video/mp4",
	".webm":  "video/webm",
}

// ContentType is a file's type from its extension.
func ContentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if ct, ok := contentTypes[ext]; ok {
		return ct
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

func contentType(f File) string {
	if f.ContentType != "" {
		return f.ContentType
	}
	return ContentType(f.Path)
}

// normal lower-cases, trims, sorts and de-duplicates both lists, and never
// returns nil slices, so meta.json always carries arrays.
func (s ShareList) normal() ShareList {
	clean := func(in []string) []string {
		out := make([]string, 0, len(in))
		for _, v := range in {
			if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
				out = append(out, v)
			}
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	return ShareList{Emails: clean(s.Emails), Domains: clean(s.Domains)}
}

func (s ShareList) check() error {
	for _, e := range s.Emails {
		if i := strings.Index(e, "@"); i <= 0 || i != strings.LastIndex(e, "@") || i == len(e)-1 || strings.ContainsAny(e, " \t,") {
			return fmt.Errorf("%w: %q is not an email address", ErrInvalid, e)
		}
	}
	for _, d := range s.Domains {
		if strings.ContainsAny(d, "@ \t,/") || !strings.Contains(d, ".") {
			return fmt.Errorf("%w: %q is not a domain", ErrInvalid, d)
		}
	}
	return nil
}

// Share adds add, then drops remove (or everything when all), writing
// meta.json with If-Match before the store.
func (p *Publisher) Share(ctx context.Context, id string, add, remove ShareList, all bool) (Meta, error) {
	add, remove = add.normal(), remove.normal()
	if err := add.check(); err != nil {
		return Meta{}, err
	}
	rec, err := p.liveRecord(ctx, id)
	if err != nil {
		return Meta{}, err
	}
	meta, etag, err := p.readMeta(ctx, rec)
	if err != nil {
		return Meta{}, err
	}
	if meta.Deleted {
		return Meta{}, fmt.Errorf("%w: %s was deleted", ErrNotFound, id)
	}
	next := ShareList{}
	if !all {
		drop := func(list, gone []string) []string {
			return slices.DeleteFunc(list, func(v string) bool { return slices.Contains(gone, v) })
		}
		next = ShareList{
			Emails:  drop(append(meta.Share.Emails, add.Emails...), remove.Emails),
			Domains: drop(append(meta.Share.Domains, add.Domains...), remove.Domains),
		}
	}
	meta.Share = next.normal()
	if err := p.putMeta(ctx, id, meta, etag); err != nil {
		return Meta{}, err
	}
	rec.Share = meta.Share
	if err := p.Store.Save(ctx, rec); err != nil {
		return Meta{}, fmt.Errorf("artifact %s share is live but not recorded: %w", id, err)
	}
	return meta, nil
}

// Delete marks meta.json and the record deleted, then removes the version
// files. Deleting a deleted artifact only retries the file removal.
func (p *Publisher) Delete(ctx context.Context, id string) error {
	rec, err := p.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	if !rec.Deleted {
		meta, etag, err := p.readMeta(ctx, rec)
		if err != nil {
			return err
		}
		meta.Deleted = true
		if err := p.putMeta(ctx, id, meta, etag); err != nil {
			return err
		}
		rec.Deleted = true
		if err := p.Store.Save(ctx, rec); err != nil {
			return fmt.Errorf("artifact %s is deleted in R2 but not recorded: %w", id, err)
		}
	}
	keys, err := p.Bucket.List(ctx, "a/"+id+"/v/")
	if err != nil {
		return fmt.Errorf("artifact %s: list version files: %w", id, err)
	}
	if err := p.Bucket.Delete(ctx, keys); err != nil {
		return fmt.Errorf("artifact %s: remove version files: %w", id, err)
	}
	return nil
}
