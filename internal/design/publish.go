// Package design publishes coordinator design pages to a docs repository.
package design

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/bouwerp/aiman/internal/infra/config"
)

const (
	checkoutRel = ".aiman/design-repo"
	lockRel     = ".aiman/design-repo.lock"
	pushTries   = 3
)

// Result describes a published page.
type Result struct {
	// Path is the page path in the docs repository, or the file path when
	// Local.
	Path string `json:"path"`
	// URL is the rendered page, empty when the site is unknown.
	URL string `json:"url,omitempty"`
	// Build is the latest GitHub Pages build status, empty when unknown.
	Build string `json:"build,omitempty"`
	// Revision counts the commits that touched Path; 0 when Local.
	Revision int `json:"revision"`
	// Local reports that no docs repository is configured and the page was
	// only copied under the session's git directory.
	Local bool `json:"local,omitempty"`
}

// ErrInvalid marks a publish refused for its input (a bad key or repository
// name) rather than failed while publishing.
var ErrInvalid = errors.New("invalid design publish")

// Publisher copies a page into a host checkout of the docs repository and
// pushes it to main. Share one Publisher per process: it remembers which
// repositories passed the visibility check.
type Publisher struct {
	Cfg config.DesignConfig
	// Home holds .aiman/design-repo and its lock.
	Home string
	// Run executes a command in dir and returns its combined output.
	Run func(ctx context.Context, dir string, name string, args ...string) (string, error)

	mu      sync.Mutex
	private map[string]bool // GitHub repositories seen private or internal
}

// Publish copies src to <design.dir>/<repoName>/<key>.md and returns where it
// went. Without a docs repository the page lands beside src. Concurrent calls
// on one host serialise on a file lock.
func (p *Publisher) Publish(ctx context.Context, src, repoName, key string) (Result, error) {
	body, err := os.ReadFile(src)
	if err != nil {
		return Result{}, fmt.Errorf("read design: %w", err)
	}
	return p.publish(ctx, body, filepath.Dir(src), repoName, key)
}

// PublishBytes publishes body, so a caller that also inspects the page works
// from exactly the bytes that were published. Without a docs repository the
// page lands under localDir, the session's git directory.
func (p *Publisher) PublishBytes(ctx context.Context, body []byte, localDir, repoName, key string) (Result, error) {
	return p.publish(ctx, body, localDir, repoName, key)
}

func safeName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`)
}

func (p *Publisher) publish(ctx context.Context, body []byte, localDir, repoName, key string) (Result, error) {
	if !safeName(key) {
		return Result{}, fmt.Errorf("%w: key %q", ErrInvalid, key)
	}
	if !safeName(repoName) {
		return Result{}, fmt.Errorf("%w: repo name %q", ErrInvalid, repoName)
	}
	if strings.TrimSpace(p.Cfg.Repo) == "" {
		return p.publishLocal(localDir, key, body)
	}
	if err := p.checkPrivate(ctx); err != nil {
		return Result{}, err
	}
	unlock, err := lockFile(ctx, filepath.Join(p.Home, lockRel))
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	return p.publishRepo(ctx, repoName, key, body)
}

// checkPrivate refuses a docs repository that is not private or internal:
// design pages describe private code. Only a local filesystem location is
// exempt. A GitHub location must report private or internal visibility (the
// first success per repository is remembered). Any other location is refused,
// because its visibility cannot be checked.
func (p *Publisher) checkPrivate(ctx context.Context) error {
	repo := strings.TrimSpace(p.Cfg.Repo)
	if isLocalLocation(repo) {
		return nil
	}
	slug, ok := githubSlug(repo)
	if !ok {
		return fmt.Errorf("%w: design repo %q: its visibility cannot be checked; use a github.com repository or a local path", ErrInvalid, repo)
	}
	p.mu.Lock()
	seen := p.private[slug]
	p.mu.Unlock()
	if seen {
		return nil
	}
	out, err := p.Run(ctx, p.Home, "gh", "api", "repos/"+slug, "-q", ".visibility")
	if err != nil {
		return fmt.Errorf("check visibility of %s: %w: %s", slug, err, firstLine(out))
	}
	switch vis := strings.ToLower(strings.TrimSpace(out)); vis {
	case "private", "internal":
	default:
		return fmt.Errorf("design repo %s is %s; design pages publish only to a private or internal repository", slug, vis)
	}
	p.mu.Lock()
	if p.private == nil {
		p.private = map[string]bool{}
	}
	p.private[slug] = true
	p.mu.Unlock()
	return nil
}

// isLocalLocation reports whether repo is a local filesystem location: an
// absolute path, a path starting with ./ or ../, or a file:// URL.
func isLocalLocation(repo string) bool {
	return filepath.IsAbs(repo) || strings.HasPrefix(repo, "./") || strings.HasPrefix(repo, "../") ||
		strings.HasPrefix(strings.ToLower(repo), "file://")
}

// githubSlugRe matches the owner/name part of a GitHub repository.
var githubSlugRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// githubSlug is the owner/name of a GitHub repository given as a slug or as a
// github.com or www.github.com location (https://, ssh://, ssh://git@ or
// git@host:), host and scheme matched case-insensitively. Anything else is
// not GitHub.
func githubSlug(repo string) (string, bool) {
	rest, ok := githubPath(repo)
	if !ok {
		return "", false
	}
	rest = strings.TrimSuffix(strings.TrimSuffix(rest, "/"), ".git")
	if !githubSlugRe.MatchString(rest) || strings.HasPrefix(rest, ".") {
		return "", false
	}
	return rest, true
}

// githubPath strips a GitHub host prefix from repo and returns the rest. A
// value with no scheme and no scp-style host is returned whole, as a slug.
func githubPath(repo string) (string, bool) {
	if user, rest, ok := strings.Cut(repo, "@"); ok && !strings.Contains(repo, "://") {
		host, path, ok := strings.Cut(rest, ":")
		return path, ok && strings.EqualFold(user, "git") && isGitHubHost(host)
	}
	if !strings.Contains(repo, "://") {
		return repo, true
	}
	u, err := url.Parse(repo)
	if err != nil || !isGitHubHost(u.Host) {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "http":
		if u.User != nil {
			return "", false
		}
	case "ssh":
		if u.User != nil && u.User.String() != "git" {
			return "", false
		}
	default:
		return "", false
	}
	return strings.TrimPrefix(u.Path, "/"), true
}

func isGitHubHost(host string) bool {
	host = strings.ToLower(host)
	return host == "github.com" || host == "www.github.com"
}

func (p *Publisher) publishLocal(base, key string, body []byte) (Result, error) {
	if base == "" {
		return Result{}, errors.New("no directory for a local design page")
	}
	dst := filepath.Join(base, "aiman-design", key+".md")
	if err := writeFile(dst, body); err != nil {
		return Result{}, err
	}
	return Result{Path: dst, Local: true}, nil
}

func writeFile(dst string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("create design dir: %w", err)
	}
	if err := os.WriteFile(dst, body, 0o600); err != nil {
		return fmt.Errorf("write design: %w", err)
	}
	return nil
}

func (p *Publisher) publishRepo(ctx context.Context, repoName, key string, body []byte) (Result, error) {
	dir := filepath.Join(p.Home, checkoutRel)
	if err := p.ensureCheckout(ctx, dir); err != nil {
		return Result{}, err
	}
	if err := p.syncCheckout(ctx, dir); err != nil {
		return Result{}, err
	}
	rel := path.Join(p.Cfg.DirOrDefault(), repoName, key+".md")
	if err := writeFile(filepath.Join(dir, filepath.FromSlash(rel)), body); err != nil {
		return Result{}, err
	}
	if _, err := p.git(ctx, dir, "add", "--", rel); err != nil {
		return Result{}, err
	}
	rev, err := p.revisions(ctx, dir, rel)
	if err != nil {
		return Result{}, err
	}
	if _, err := p.git(ctx, dir, "diff", "--cached", "--quiet", "--", rel); err != nil {
		rev++
		msg := fmt.Sprintf("design(%s): %s rev %d", repoName, key, rev)
		if _, err := p.git(ctx, dir, "commit", "-m", msg, "--", rel); err != nil {
			return Result{}, err
		}
		if err := p.push(ctx, dir); err != nil {
			// Drop the unpushed commit so it does not inflate the next revision.
			_ = p.syncCheckout(ctx, dir)
			return Result{}, err
		}
	}
	return Result{Path: rel, URL: p.pageURL(ctx, dir, rel), Build: p.build(ctx, dir), Revision: rev}, nil
}

// syncCheckout makes the aiman-owned checkout match origin/main exactly:
// whatever an earlier failed publish left (a rebase in progress, a staged
// change, an unpushed commit) is discarded.
func (p *Publisher) syncCheckout(ctx context.Context, dir string) error {
	_, _ = p.git(ctx, dir, "rebase", "--abort")
	if _, err := p.git(ctx, dir, "fetch", "origin"); err != nil {
		return err
	}
	if _, err := p.git(ctx, dir, "reset", "--hard", "origin/main"); err != nil {
		return err
	}
	_, err := p.git(ctx, dir, "clean", "-fd")
	return err
}

// revisions counts the commits on HEAD that touched rel.
func (p *Publisher) revisions(ctx context.Context, dir, rel string) (int, error) {
	out, err := p.git(ctx, dir, "log", "--format=%H", "--", rel)
	if err != nil {
		if strings.Contains(out, "does not have any commits") {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n, nil
}

// push sends HEAD to main; a rejected push rebases and tries again.
func (p *Publisher) push(ctx context.Context, dir string) error {
	var last error
	for i := 0; i < pushTries; i++ {
		if _, last = p.git(ctx, dir, "push", "origin", "HEAD:main"); last == nil {
			return nil
		}
		if _, err := p.git(ctx, dir, "pull", "--rebase"); err != nil {
			return err
		}
	}
	return last
}

func (p *Publisher) ensureCheckout(ctx context.Context, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat design checkout: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return fmt.Errorf("create design checkout parent: %w", err)
	}
	parent := filepath.Dir(dir)
	repo := strings.TrimSpace(p.Cfg.Repo)
	var out string
	var err error
	if isGitLocation(repo) {
		out, err = p.Run(ctx, parent, "git", "clone", repo, dir)
	} else {
		out, err = p.Run(ctx, parent, "gh", "repo", "clone", repo, dir)
	}
	if err != nil {
		return fmt.Errorf("clone design repo %s: %w: %s", repo, err, firstLine(out))
	}
	return nil
}

// isGitLocation reports a filesystem path or URL, which git clones directly;
// anything else is an owner/name slug for gh.
func isGitLocation(repo string) bool {
	return strings.Contains(repo, "://") || strings.HasPrefix(repo, "git@") ||
		filepath.IsAbs(repo) || strings.HasPrefix(repo, ".")
}

func (p *Publisher) git(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-c", "user.name=aiman", "-c", "user.email=aiman@localhost"}, args...)
	out, err := p.Run(ctx, dir, "git", full...)
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", args[0], err, firstLine(out))
	}
	return out, nil
}

// pageURL is the rendered page: the site base (configured, else asked from
// GitHub Pages) plus the page path without its leading docs/ and with .html.
func (p *Publisher) pageURL(ctx context.Context, dir, rel string) string {
	base := strings.TrimSpace(p.Cfg.Site)
	if base == "" {
		if out, err := p.Run(ctx, dir, "gh", "api", "repos/"+p.Cfg.Repo+"/pages", "-q", ".html_url"); err == nil {
			base = strings.TrimSpace(out)
		}
	}
	if base == "" {
		return ""
	}
	page := strings.TrimSuffix(strings.TrimPrefix(rel, "docs/"), ".md") + ".html"
	return strings.TrimRight(base, "/") + "/" + page
}

// build is the latest Pages build status, empty when gh cannot say.
func (p *Publisher) build(ctx context.Context, dir string) string {
	out, err := p.Run(ctx, dir, "gh", "api", "repos/"+p.Cfg.Repo+"/pages/builds/latest", "-q", ".status")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	line, _, _ := strings.Cut(s, "\n")
	return line
}
