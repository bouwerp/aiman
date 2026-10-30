package review

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Hunk is the added and modified lines of one unified-diff hunk.
type Hunk struct {
	Path  string
	Lines []int
}

var (
	hunkHeaderRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
	goCoverRe    = regexp.MustCompile(`^(.+):(\d+)\.\d+,(\d+)\.\d+ \d+ (\d+)$`)
)

// ChangedLines returns added and modified lines from a unified diff.
// Deletions are ignored. A rename uses the new path.
func ChangedLines(diff string) []Hunk {
	var out []Hunk
	cur := -1
	newLine := 0
	skip := false
	sc := bufio.NewScanner(strings.NewReader(diff))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "+++ "):
			path, ok := plusPlusPath(line)
			if !ok {
				skip, cur = true, -1
				continue
			}
			skip = false
			out = append(out, Hunk{Path: path})
			cur = len(out) - 1
		case strings.HasPrefix(line, "@@ "):
			if skip || cur < 0 {
				continue
			}
			n, ok := hunkNewStart(line)
			if !ok {
				continue
			}
			if len(out[cur].Lines) > 0 {
				out = append(out, Hunk{Path: out[cur].Path})
				cur = len(out) - 1
			}
			newLine = n
		case skip || cur < 0:
			continue
		case strings.HasPrefix(line, "+"):
			out[cur].Lines = append(out[cur].Lines, newLine)
			newLine++
		case strings.HasPrefix(line, "-"):
			continue
		case strings.HasPrefix(line, "\\"):
			continue
		default:
			newLine++
		}
	}
	n := 0
	for _, h := range out {
		if len(h.Lines) == 0 {
			continue
		}
		out[n] = h
		n++
	}
	return out[:n]
}

func plusPlusPath(line string) (string, bool) {
	rest := strings.TrimPrefix(line, "+++ ")
	if i := strings.IndexByte(rest, '\t'); i >= 0 {
		rest = rest[:i]
	}
	rest = strings.TrimSpace(rest)
	if rest == "/dev/null" {
		return "", false
	}
	rest = strings.TrimPrefix(rest, "b/")
	if rest == "" || rest == "/dev/null" {
		return "", false
	}
	return rest, true
}

func hunkNewStart(line string) (int, bool) {
	m := hunkHeaderRe.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// GoCoverage reads a Go cover profile. Paths under /src/ become
// repository-relative. A line is covered when any block that includes it
// has a count greater than zero.
func GoCoverage(profile string) (map[string]map[int]bool, error) {
	f, err := os.Open(profile) //nolint:gosec // profile is a review cover file
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]map[int]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		m := goCoverRe.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("review coverage: bad cover profile line %q", line)
		}
		start, _ := strconv.Atoi(m[2])
		end, _ := strconv.Atoi(m[3])
		count, _ := strconv.Atoi(m[4])
		markCover(out, stripSrc(m[1]), start, end, count > 0)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// IstanbulCoverage reads nyc/vitest coverage-final.json. Paths are made
// relative to root, then any leftover /src/ prefix is stripped.
func IstanbulCoverage(data []byte, root string) (map[string]map[int]bool, error) {
	var files map[string]istanbulFile
	if err := json.Unmarshal(data, &files); err != nil {
		return nil, fmt.Errorf("review coverage: istanbul: %w", err)
	}
	out := map[string]map[int]bool{}
	for key, f := range files {
		path := f.Path
		if path == "" {
			path = key
		}
		path = repoRel(path, root)
		for id, span := range f.StatementMap {
			start, end := span.Start.Line, span.End.Line
			if end < start {
				end = start
			}
			markCover(out, path, start, end, f.S[id] > 0)
		}
	}
	return out, nil
}

type istanbulFile struct {
	Path         string                  `json:"path"`
	StatementMap map[string]istanbulSpan `json:"statementMap"`
	S            map[string]int          `json:"s"`
}

type istanbulSpan struct {
	Start istanbulPos `json:"start"`
	End   istanbulPos `json:"end"`
}

type istanbulPos struct {
	Line int `json:"line"`
}

func markCover(out map[string]map[int]bool, path string, start, end int, covered bool) {
	m := out[path]
	if m == nil {
		m = map[int]bool{}
		out[path] = m
	}
	for n := start; n <= end; n++ {
		m[n] = m[n] || covered
	}
}

func stripSrc(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	return strings.TrimPrefix(path, "/src/")
}

// RelocateCoverage rewrites profile keys from a Go module import path to
// repository-relative paths. Keys that do not start with module are unchanged.
func RelocateCoverage(cov map[string]map[int]bool, module string) map[string]map[int]bool {
	if len(cov) == 0 {
		return cov
	}
	module = strings.Trim(strings.ReplaceAll(module, "\\", "/"), "/")
	out := make(map[string]map[int]bool, len(cov))
	for p, lines := range cov {
		out[trimModule(p, module)] = lines
	}
	return out
}

func trimModule(path, module string) string {
	if module == "" {
		return path
	}
	if path == module {
		return "."
	}
	if strings.HasPrefix(path, module+"/") {
		return strings.TrimPrefix(path, module+"/")
	}
	return path
}

// ModulePath is the `module` line of src/go.mod, or empty when none.
func ModulePath(src string) string {
	raw, err := os.ReadFile(filepath.Join(src, "go.mod")) //nolint:gosec // src is the sandbox export
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func repoRel(path, root string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	root = strings.ReplaceAll(strings.TrimSuffix(root, "/"), "\\", "/")
	if root != "" {
		if path == root {
			return "."
		}
		if strings.HasPrefix(path, root+"/") {
			return strings.TrimPrefix(path, root+"/")
		}
	}
	return stripSrc(path)
}

// CoverageReport lists each hunk's ran and missed changed lines, then totals.
func CoverageReport(h []Hunk, cov map[string]map[int]bool) (string, int, int) {
	var b strings.Builder
	b.WriteString("# Coverage\n")
	ran, missed := 0, 0
	for _, hunk := range h {
		var ranL, missL []int
		file := cov[hunk.Path]
		for _, n := range hunk.Lines {
			if file[n] {
				ranL = append(ranL, n)
				ran++
				continue
			}
			missL = append(missL, n)
			missed++
		}
		fmt.Fprintf(&b, "\n## %s\nran: %s\nmissed: %s\n", hunk.Path, joinNums(ranL), joinNums(missL))
	}
	fmt.Fprintf(&b, "\nran: %d missed: %d\n", ran, missed)
	return b.String(), ran, missed
}

func joinNums(n []int) string {
	if len(n) == 0 {
		return "none"
	}
	parts := make([]string, len(n))
	for i, v := range n {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ", ")
}
