package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Browser source maps: the tester debugs the frontend in their own browser's
// DevTools through the forwarded app port, and DevTools shows the original
// .ts and .tsx files only when the served JavaScript names a source map.
// Serve checks that once a run is ready and stores the answer on the run.

// BrowserSourcemaps states.
const (
	SourcemapsPresent = "present"
	SourcemapsAbsent  = "absent"
	SourcemapsUnknown = "unknown"
)

// How the answer was found.
const (
	// SourcemapMethodHTTP fetched the app's page and its scripts through the
	// edge, as the tester's browser does.
	SourcemapMethodHTTP = "http"
	// SourcemapMethodScan read the build output in the run's exported tree.
	SourcemapMethodScan = "scan"
)

// maxSourcemapExamples caps the files a result names.
const maxSourcemapExamples = 3

// BrowserSourcemaps is whether the app's frontend JavaScript carries source
// maps a browser can load.
type BrowserSourcemaps struct {
	State  string `json:"state"`
	Method string `json:"method,omitempty"`
	// Port is the app port whose page was read (http only).
	Port string `json:"port,omitempty"`
	// Hidden is set when the build wrote .map files but no script names
	// them (Vite's build.sourcemap: 'hidden'), so the browser finds none.
	Hidden bool `json:"hidden,omitempty"`
	// Files are up to three scripts the answer rests on: URL paths for http,
	// tree paths for scan.
	Files  []string `json:"files,omitempty"`
	Detail string   `json:"detail,omitempty"`
}

// SourcemapTarget is one app port as serve reaches it: the edge's published
// port on the host.
type SourcemapTarget struct {
	Port string
	URL  string
}

// SourcemapLimits bound the detection. The app under test is untrusted, so
// every read is capped.
type SourcemapLimits struct {
	// Timeout bounds the whole detection; the page probe gets three quarters.
	Timeout time.Duration
	// MaxPorts is how many app ports are probed.
	MaxPorts int
	// MaxScripts is how many same-origin scripts of the page are fetched.
	MaxScripts int
	// MaxBody caps the bytes read from any one response.
	MaxBody int64
	// MaxFiles caps the directory entries the scan visits.
	MaxFiles int
	// TailBytes is how much of the end of each JavaScript file the scan reads.
	TailBytes int64
	// MaxTotalBytes caps the bytes the scan reads in all.
	MaxTotalBytes int64
}

// DefaultSourcemapLimits are the limits serve uses.
func DefaultSourcemapLimits() SourcemapLimits {
	return SourcemapLimits{
		Timeout:       20 * time.Second,
		MaxPorts:      4,
		MaxScripts:    4,
		MaxBody:       8 << 20,
		MaxFiles:      5000,
		TailBytes:     64 << 10,
		MaxTotalBytes: 16 << 20,
	}
}

// DetectSourcemaps asks each app port's page first, since that is what the
// browser loads, and reads the build output in src when no page answers.
func DetectSourcemaps(ctx context.Context, src string, targets []SourcemapTarget, lim SourcemapLimits) BrowserSourcemaps {
	ctx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel()
	probeCtx, cancelProbe := context.WithTimeout(ctx, lim.Timeout*3/4)
	defer cancelProbe()
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	defer client.CloseIdleConnections()
	if len(targets) > lim.MaxPorts {
		targets = targets[:lim.MaxPorts]
	}
	for _, t := range targets {
		if r := ProbeSourcemaps(probeCtx, client, t.URL, lim); r.State != SourcemapsUnknown {
			r.Port = t.Port
			return r
		}
	}
	return ScanSourcemaps(ctx, src, lim)
}

// checkSourcemaps runs after a run turns ready and stores the answer on the
// live run, unless the run stopped meanwhile. ctx is the start's, which a
// Down cancels.
func (m *Manager) checkSourcemaps(ctx context.Context, lr *liveRun, src string) {
	run := lr.snapshot()
	if run.State != StateReady {
		return
	}
	var targets []SourcemapTarget
	for _, p := range run.Ports {
		if p.Service == ServiceApp && p.Name != RelayDAP {
			targets = append(targets, SourcemapTarget{Port: p.Name, URL: "http://127.0.0.1:" + strconv.Itoa(p.HostPort)})
		}
	}
	detect := m.opts.Sourcemaps
	if detect == nil {
		detect = func(ctx context.Context, src string, targets []SourcemapTarget) BrowserSourcemaps {
			return DetectSourcemaps(ctx, src, targets, DefaultSourcemapLimits())
		}
	}
	got := detect(ctx, src, targets)
	lr.mu.Lock()
	if lr.stopping || lr.run.State != StateReady || ctx.Err() != nil {
		lr.mu.Unlock()
		return
	}
	defer lr.mu.Unlock()
	lr.run.BrowserSourcemaps = &got
	// Written under the run's lock, as a verdict is, so neither write
	// replaces the other's run.json with an older run.
	if err := m.writeRun(lr.run); err != nil {
		m.logf("write run %s: %v", lr.run.ID, err)
	}
}

// ---- page probe ----

var (
	scriptTagRe  = regexp.MustCompile(`(?is)<script\b[^>]*>`)
	linkTagRe    = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	srcAttrRe    = regexp.MustCompile(`(?is)\bsrc\s*=\s*["']([^"']+)["']`)
	hrefAttrRe   = regexp.MustCompile(`(?is)\bhref\s*=\s*["']([^"']+)["']`)
	preloadRe    = regexp.MustCompile(`(?is)\brel\s*=\s*["']?modulepreload\b`)
	mapCommentRe = regexp.MustCompile(`(?m)^[ \t]*//[#@][ \t]*sourceMappingURL=([^\s'"]+)[ \t]*\r?$`)
)

// probeTally counts the scripts a probe read.
type probeTally struct {
	present   []string
	none      int
	undecided int
	missing   string
}

// ProbeSourcemaps reads base's page and up to MaxScripts of its same-origin
// scripts and their maps. It follows no redirect and fetches nothing from
// another origin.
func ProbeSourcemaps(ctx context.Context, client *http.Client, base string, lim SourcemapLimits) BrowserSourcemaps {
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	origin, err := url.Parse(strings.TrimRight(base, "/") + "/")
	if err != nil {
		return unknownSourcemaps("bad app URL: " + err.Error())
	}
	page, err := fetchCapped(ctx, &c, origin.String(), lim.MaxBody)
	if err != nil {
		return unknownSourcemaps("GET / failed: " + err.Error())
	}
	if !page.ok() {
		return unknownSourcemaps("GET / answered " + strconv.Itoa(page.status))
	}
	scripts := pageScripts(page.body, origin, lim.MaxScripts)
	if len(scripts) == 0 {
		return unknownSourcemaps("the page at / loads no script from its own origin")
	}
	var t probeTally
	for _, s := range scripts {
		probeScript(ctx, &c, origin, s, lim, &t)
		if len(t.present) > 0 {
			return BrowserSourcemaps{State: SourcemapsPresent, Method: SourcemapMethodHTTP, Files: t.present}
		}
	}
	switch {
	case t.undecided > 0:
		return unknownSourcemaps(fmt.Sprintf("%d of the page's scripts could not be read to the end", t.undecided))
	case t.missing != "":
		return BrowserSourcemaps{State: SourcemapsAbsent, Method: SourcemapMethodHTTP, Detail: t.missing}
	default:
		return BrowserSourcemaps{State: SourcemapsAbsent, Method: SourcemapMethodHTTP,
			Detail: fmt.Sprintf("none of the %d scripts the page loads names a source map", t.none)}
	}
}

func unknownSourcemaps(detail string) BrowserSourcemaps {
	return BrowserSourcemaps{State: SourcemapsUnknown, Detail: detail}
}

// probeScript fetches one script and, when it names a map file, the map.
func probeScript(ctx context.Context, c *http.Client, origin, script *url.URL, lim SourcemapLimits, t *probeTally) {
	resp, err := fetchCapped(ctx, c, script.String(), lim.MaxBody)
	if err != nil || !resp.ok() {
		t.undecided++
		return
	}
	ref := resp.header.Get("SourceMap")
	if ref == "" {
		ref = resp.header.Get("X-SourceMap")
	}
	if ref == "" {
		ref = lastMapComment(resp.body)
	}
	switch {
	case ref == "" && resp.truncated:
		t.undecided++
	case ref == "":
		t.none++
	case strings.HasPrefix(ref, "data:"):
		t.present = append(t.present, script.Path)
	default:
		probeMap(ctx, c, origin, script, ref, lim, t)
	}
}

func probeMap(ctx context.Context, c *http.Client, origin, script *url.URL, ref string, lim SourcemapLimits, t *probeTally) {
	u, err := script.Parse(ref)
	if err != nil || !sameOrigin(u, origin) {
		t.undecided++
		return
	}
	resp, err := fetchCapped(ctx, c, u.String(), min(lim.MaxBody, 1024))
	if err != nil {
		t.undecided++
		return
	}
	if resp.ok() {
		t.present = append(t.present, script.Path)
		return
	}
	t.missing = fmt.Sprintf("%s names the source map %s, which answers %d", script.Path, u.Path, resp.status)
}

type cappedResponse struct {
	status    int
	header    http.Header
	body      []byte
	truncated bool
}

func (r cappedResponse) ok() bool { return r.status >= 200 && r.status < 300 }

// fetchCapped GETs u and reads at most limit bytes of the body.
func fetchCapped(ctx context.Context, c *http.Client, u string, limit int64) (cappedResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return cappedResponse{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return cappedResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return cappedResponse{}, err
	}
	out := cappedResponse{status: resp.StatusCode, header: resp.Header, body: body}
	if int64(len(body)) > limit {
		out.body, out.truncated = body[:limit], true
	}
	return out, nil
}

// pageScripts lists the page's script src and modulepreload href URLs that
// share origin's scheme and host, in page order, without repeats.
func pageScripts(page []byte, origin *url.URL, limit int) []*url.URL {
	var refs []string
	for _, tag := range scriptTagRe.FindAll(page, -1) {
		if m := srcAttrRe.FindSubmatch(tag); m != nil {
			refs = append(refs, string(m[1]))
		}
	}
	for _, tag := range linkTagRe.FindAll(page, -1) {
		if !preloadRe.Match(tag) {
			continue
		}
		if m := hrefAttrRe.FindSubmatch(tag); m != nil {
			refs = append(refs, string(m[1]))
		}
	}
	seen := map[string]bool{}
	var out []*url.URL
	for _, ref := range refs {
		u, err := origin.Parse(strings.TrimSpace(ref))
		if err != nil || !sameOrigin(u, origin) || seen[u.String()] {
			continue
		}
		seen[u.String()] = true
		out = append(out, u)
		if len(out) == limit {
			break
		}
	}
	return out
}

func sameOrigin(u, origin *url.URL) bool {
	return u.Scheme == origin.Scheme && u.Host == origin.Host
}

// lastMapComment is the URL of the last sourceMappingURL comment in b.
func lastMapComment(b []byte) string {
	all := mapCommentRe.FindAllSubmatch(b, -1)
	if len(all) == 0 {
		return ""
	}
	return string(all[len(all)-1][1])
}

// ---- tree scan ----

// buildDirs are the directory names whose JavaScript the scan reads.
var buildDirs = map[string]bool{"dist": true, "build": true, "out": true}

// maxSearchDepth is how deep the scan looks for a build directory.
const maxSearchDepth = 4

var errScanBudget = errors.New("scan budget spent")

type treeScan struct {
	ctx     context.Context
	root    *os.Root
	lim     SourcemapLimits
	visited int
	read    int64
	stopped bool
	// js counts scripts by what their end said.
	withMap, without, undecided int
	maps                        int
	examples                    []string
}

// ScanSourcemaps reads the end of each .js and .mjs file under a dist, build
// or out directory of src (node_modules and dot directories skipped) for a
// sourceMappingURL comment. It never follows a symlink and reads only
// regular files.
func ScanSourcemaps(ctx context.Context, src string, lim SourcemapLimits) BrowserSourcemaps {
	root, err := os.OpenRoot(src)
	if err != nil {
		return unknownSourcemaps("cannot read the exported tree: " + err.Error())
	}
	defer root.Close()
	s := &treeScan{ctx: ctx, root: root, lim: lim}
	if err := fs.WalkDir(root.FS(), ".", s.visit); err != nil && !errors.Is(err, errScanBudget) {
		return unknownSourcemaps("scan stopped: " + err.Error())
	}
	return s.result()
}

func (s *treeScan) visit(path string, d fs.DirEntry, err error) error {
	if cerr := s.ctx.Err(); cerr != nil {
		return cerr
	}
	if err != nil {
		if d != nil && d.IsDir() {
			return fs.SkipDir
		}
		return nil
	}
	s.visited++
	if s.visited > s.lim.MaxFiles {
		s.stopped = true
		return errScanBudget
	}
	inBuild := inBuildDir(path, d.IsDir())
	if d.IsDir() {
		if path == "." {
			return nil
		}
		name := d.Name()
		if name == "node_modules" || strings.HasPrefix(name, ".") {
			return fs.SkipDir
		}
		if !inBuild && strings.Count(path, "/")+1 >= maxSearchDepth {
			return fs.SkipDir
		}
		return nil
	}
	if !inBuild || !d.Type().IsRegular() {
		return nil
	}
	name := d.Name()
	switch {
	case strings.HasSuffix(name, ".js.map") || strings.HasSuffix(name, ".mjs.map"):
		s.maps++
	case strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".mjs"):
		return s.checkJS(path)
	}
	return nil
}

// inBuildDir reports whether path lies in a build directory (a directory
// path counts when it is one).
func inBuildDir(path string, isDir bool) bool {
	parts := strings.Split(path, "/")
	if !isDir {
		parts = parts[:len(parts)-1]
	}
	for _, p := range parts {
		if buildDirs[p] {
			return true
		}
	}
	return false
}

func (s *treeScan) checkJS(path string) error {
	f, err := s.root.OpenFile(path, os.O_RDONLY|openNonBlock, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	n := min(fi.Size(), s.lim.TailBytes)
	if s.read+n > s.lim.MaxTotalBytes {
		s.stopped = true
		return errScanBudget
	}
	s.read += n
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, fi.Size()-n); err != nil && !errors.Is(err, io.EOF) {
		return nil
	}
	switch {
	case lastMapComment(buf) != "":
		s.withMap++
		if len(s.examples) < maxSourcemapExamples {
			s.examples = append(s.examples, path)
		}
	case fi.Size() > n && looksLikeBase64Tail(buf):
		// The end of a long inline map: its comment starts before the window.
		s.undecided++
	default:
		s.without++
	}
	return nil
}

// looksLikeBase64Tail reports whether b holds no line break and only base64
// characters, as the end of an inline data: source map does.
func looksLikeBase64Tail(b []byte) bool {
	b = bytes.TrimRight(b, " \t\r\n")
	if len(b) == 0 || bytes.ContainsAny(b, "\n") {
		return false
	}
	for _, c := range b {
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '+' && c != '/' && c != '=' {
			return false
		}
	}
	return true
}

func (s *treeScan) result() BrowserSourcemaps {
	switch {
	case s.withMap > 0:
		return BrowserSourcemaps{State: SourcemapsPresent, Method: SourcemapMethodScan, Files: s.examples}
	case s.stopped:
		return unknownSourcemaps(fmt.Sprintf("the scan stopped after %d files without finding a source map", s.visited-1))
	case s.undecided > 0:
		return unknownSourcemaps(fmt.Sprintf("%d build files end in data the scan could not read to its start", s.undecided))
	case s.without > 0:
		r := BrowserSourcemaps{State: SourcemapsAbsent, Method: SourcemapMethodScan, Hidden: s.maps > 0,
			Detail: fmt.Sprintf("none of the %d JavaScript files in the build output names a source map", s.without)}
		if r.Hidden {
			r.Detail += fmt.Sprintf(", though %d .map files exist", s.maps)
		}
		return r
	default:
		return unknownSourcemaps("no app port served a page with scripts, and the tree has no JavaScript under a dist, build or out directory")
	}
}
