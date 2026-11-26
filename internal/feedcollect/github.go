package feedcollect

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/redact"
)

// GHFunc runs `gh` with args and returns its stdout. On a non-zero exit it
// returns what gh printed with the error, because `gh api --include` prints
// the status line and headers of a 304 before it exits 1.
type GHFunc func(ctx context.Context, args ...string) ([]byte, error)

// GitHubEvery is how often the GitHub source polls.
const GitHubEvery = 2 * time.Minute

const (
	ghSinceCursor = "since"
	ghETagCursor  = "etag"
	ghPerPage     = 50
)

// ghReasons are the notification reasons the feed reads, and whether each is
// urgent (it can start a batch early).
var ghReasons = map[string]bool{
	"review_requested": true,
	"mention":          true,
	"team_mention":     true,
	"assign":           false,
	"comment":          false,
}

type githubSource struct{ gh GHFunc }

// NewGitHubSource reads the member's GitHub notifications through the host's
// gh, conditionally on the last ETag.
func NewGitHubSource(gh GHFunc) Source { return &githubSource{gh: gh} }

func (g *githubSource) Name() domain.FeedSource { return "github" }
func (g *githubSource) Every() time.Duration    { return GitHubEvery }

type ghNotification struct {
	ID        string `json:"id"`
	Reason    string `json:"reason"`
	UpdatedAt string `json:"updated_at"`
	Subject   struct {
		Title string `json:"title"`
		URL   string `json:"url"`
		Type  string `json:"type"`
	} `json:"subject"`
	Repository struct {
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
	} `json:"repository"`
}

// ghMaxPages bounds one poll; the notifications endpoint is paged by 50.
const ghMaxPages = 10

// Poll asks for notifications updated since the cursor, sending the last
// ETag with the first page. A 304 returns no events and the cursors it was
// given. Pages are read until a short one, at most ghMaxPages.
func (g *githubSource) Poll(ctx context.Context, cursors map[string]string, mutes []domain.FeedMute) ([]domain.FeedEvent, map[string]string, error) {
	next := maps.Clone(cursors)
	if next == nil {
		next = map[string]string{}
	}
	since := cursors[ghSinceCursor]
	var evs []domain.FeedEvent
	for page := 1; page <= ghMaxPages; page++ {
		q := url.Values{}
		q.Set("per_page", strconv.Itoa(ghPerPage))
		q.Set("page", strconv.Itoa(page))
		if s := cursors[ghSinceCursor]; s != "" {
			q.Set("since", s)
		}
		args := []string{"api", "--include", "--method", "GET"}
		if etag := cursors[ghETagCursor]; etag != "" && page == 1 {
			args = append(args, "-H", "If-None-Match: "+etag)
		}
		args = append(args, "notifications?"+q.Encode())

		out, runErr := g.gh(ctx, args...)
		status, headers, body, ok := splitInclude(out)
		switch {
		case ok && status == 304 && page == 1:
			return nil, maps.Clone(cursors), nil
		case runErr != nil:
			if ok {
				return nil, nil, fmt.Errorf("gh api notifications: HTTP %d: %w", status, runErr)
			}
			return nil, nil, fmt.Errorf("gh api notifications: %w", runErr)
		case ok && (status < 200 || status > 299):
			return nil, nil, fmt.Errorf("gh api notifications: HTTP %d", status)
		case !ok:
			body = out
		}
		var ns []ghNotification
		if err := json.Unmarshal(body, &ns); err != nil {
			return nil, nil, fmt.Errorf("gh api notifications: decode: %w", err)
		}
		if etag := headers.Get("Etag"); etag != "" && page == 1 {
			next[ghETagCursor] = etag
		}
		for _, n := range ns {
			if n.UpdatedAt > since {
				since = n.UpdatedAt
			}
			urgent, read := ghReasons[n.Reason]
			if !read || n.ID == "" {
				continue
			}
			ev := ghEvent(n, urgent)
			g.enrich(ctx, n, &ev)
			evs = append(evs, finish(ev, g.Name()))
		}
		if len(ns) < ghPerPage {
			break
		}
	}
	if since != "" {
		next[ghSinceCursor] = since
	}
	return unmuted(evs, mutes), next, nil
}

const (
	ghBodyMax  = 2048
	ghFilesMax = 50
)

// enrich adds the PR's title, body and changed paths and sets the author as
// actor. It is best effort: any failed read leaves the event as it is.
func (g *githubSource) enrich(ctx context.Context, n ghNotification, ev *domain.FeedEvent) {
	if n.Subject.Type != "PullRequest" || n.Repository.FullName == "" {
		return
	}
	number, kind := ghSubjectNumber(n.Subject.URL)
	if number <= 0 || kind != "pulls" {
		return
	}
	base := fmt.Sprintf("repos/%s/pulls/%d", n.Repository.FullName, number)
	raw, err := g.gh(ctx, "api", "--method", "GET", base)
	if err != nil {
		return
	}
	var pr struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if json.Unmarshal(raw, &pr) != nil {
		return
	}
	raw, err = g.gh(ctx, "api", "--method", "GET", base+"/files?per_page=50")
	if err != nil {
		return
	}
	var files []struct {
		Filename string `json:"filename"`
	}
	if json.Unmarshal(raw, &files) != nil {
		return
	}
	text := pr.Body
	if len(text) > ghBodyMax {
		text = strings.ToValidUTF8(text[:ghBodyMax], "")
	}
	lines := []string{"Title: " + pr.Title}
	lines = append(lines, strings.Split(text, "\n")...)
	if len(files) > 0 {
		lines = append(lines, "Changed files:")
		for i, f := range files {
			if i >= ghFilesMax {
				break
			}
			lines = append(lines, "- "+f.Filename)
		}
	}
	red, _ := redact.Lines(lines, nil)
	ev.Enrichment = strings.Join(red, "\n")
	if login, _ := redact.Line(pr.User.Login, nil); login != "" {
		ev.Actor = login
	}
}

func ghEvent(n ghNotification, urgent bool) domain.FeedEvent {
	at, err := time.Parse(time.RFC3339, n.UpdatedAt)
	if err != nil {
		at = time.Now()
	}
	repo := n.Repository.FullName
	number, kind := ghSubjectNumber(n.Subject.URL)
	link := n.Repository.HTMLURL
	switch {
	case number > 0 && kind == "pulls" && repo != "":
		link = fmt.Sprintf("https://github.com/%s/pull/%d", repo, number)
	case number > 0 && kind == "issues" && repo != "":
		link = fmt.Sprintf("https://github.com/%s/issues/%d", repo, number)
	}
	ref := repo
	if number > 0 {
		ref = fmt.Sprintf("%s#%d", repo, number)
	}
	return domain.FeedEvent{
		ExternalID: n.ID + "@" + n.UpdatedAt,
		At:         at,
		Kind:       n.Reason,
		Title:      n.Subject.Title,
		Body:       fmt.Sprintf("%s on %s %s: %s", n.Reason, n.Subject.Type, ref, n.Subject.Title),
		URL:        link,
		ThreadKey:  domain.FeedThreadKeyGitHub(repo, number, n.ID),
		Class:      "direct",
		Urgent:     urgent,
	}
}

// ghSubjectNumber reads the number and collection (pulls, issues) from a
// subject API URL such as https://api.github.com/repos/o/r/pulls/42.
func ghSubjectNumber(apiURL string) (int, string) {
	u, err := url.Parse(apiURL)
	if err != nil {
		return 0, ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return 0, ""
	}
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || n <= 0 {
		return 0, ""
	}
	kind := parts[len(parts)-2]
	if kind != "pulls" && kind != "issues" {
		return 0, ""
	}
	return n, kind
}

type ghHeaders map[string]string

func (h ghHeaders) Get(k string) string { return h[strings.ToLower(k)] }

// splitInclude parses `gh api --include` output: a status line, headers, a
// blank line and the body. ok is false when out does not start with a status
// line.
func splitInclude(out []byte) (status int, headers ghHeaders, body []byte, ok bool) {
	if !bytes.HasPrefix(out, []byte("HTTP/")) {
		return 0, nil, nil, false
	}
	r := bufio.NewReader(bytes.NewReader(out))
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return 0, nil, nil, false
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, nil, nil, false
	}
	status, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, nil, nil, false
	}
	headers = ghHeaders{}
	for {
		line, err := r.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break
		}
		if k, v, found := strings.Cut(trimmed, ":"); found {
			headers[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
		if err != nil {
			break
		}
	}
	rest := new(bytes.Buffer)
	_, _ = rest.ReadFrom(r)
	return status, headers, rest.Bytes(), true
}
