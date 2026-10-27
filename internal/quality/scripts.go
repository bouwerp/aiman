package quality

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

// Output markers. Each script prints its own marker lines so login-shell
// noise ahead of them is ignored.
const (
	headerPrefix = "==> "
	homeSource   = "home"
	matchPrefix  = "MATCH "
	sumPrefix    = "SUM "
)

// RawChecklist is one checklist file as ListScript printed it.
type RawChecklist struct {
	Source  string
	Path    string
	Content []byte
}

// Name is the file base name without .md.
func (r RawChecklist) Name() string {
	return strings.TrimSuffix(path.Base(r.Path), ".md")
}

// ListScript prints "==> home $HOME", then for each *.md in the worktree's
// and the user's checklist directories a "==> <source> <abs path>" line
// followed by the first 16 KiB of the file and a newline.
func ListScript(worktree string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "printf '%s%s %%s\\n' \"$HOME\"\n", headerPrefix, homeSource)
	writeListLoop(&sb, SourceRepo, shellQuote(worktree)+"/"+shellQuote(RepoChecklistDir))
	writeListLoop(&sb, SourceUser, `"$HOME"/`+shellQuote(UserChecklistDir))
	return sb.String()
}

func writeListLoop(sb *strings.Builder, source, dir string) {
	fmt.Fprintf(sb, "for f in %s/*.md; do [ -f \"$f\" ] || continue; printf '%s%s %%s\\n' \"$f\"; head -c %d -- \"$f\"; printf '\\n'; done\n",
		dir, headerPrefix, source, maxChecklistBytes)
}

// ParseListOutput reads ListScript output. A checklist body that itself
// contains a line starting with "==> " is cut at that line.
func ParseListOutput(out string) (home string, files []RawChecklist) {
	var cur *RawChecklist
	var body strings.Builder
	flush := func() {
		if cur == nil {
			return
		}
		cur.Content = []byte(strings.TrimSuffix(body.String(), "\n"))
		files = append(files, *cur)
		cur = nil
		body.Reset()
	}
	for _, line := range strings.SplitAfter(out, "\n") {
		if rest, ok := strings.CutPrefix(line, headerPrefix); ok {
			source, p, _ := strings.Cut(strings.TrimSuffix(rest, "\n"), " ")
			switch source {
			case homeSource:
				flush()
				home = p
				continue
			case SourceRepo, SourceUser:
				flush()
				cur = &RawChecklist{Source: source, Path: p}
				continue
			}
		}
		if cur != nil {
			body.WriteString(line)
		}
	}
	flush()
	return home, files
}

// MatchScript prints "MATCH <name>" for each checklist with globs that
// matches at least one tracked or untracked, non-ignored file in the
// worktree. A user checklist shadowed by a repo checklist of the same name
// is not matched. It returns "" when no checklist has globs.
func MatchScript(worktree string, lists []domain.Checklist) string {
	repoNames := map[string]bool{}
	for _, c := range lists {
		if c.Source == SourceRepo {
			repoNames[c.Name] = true
		}
	}
	var sb strings.Builder
	for _, c := range lists {
		if len(c.AppliesTo) == 0 || (c.Source != SourceRepo && repoNames[c.Name]) {
			continue
		}
		var specs []string
		for _, p := range PathspecsFor(c.AppliesTo) {
			specs = append(specs, shellQuote(p))
		}
		fmt.Fprintf(&sb, "if [ -n \"$(git -C %s ls-files --cached --others --exclude-standard -- %s 2>/dev/null | head -n1)\" ]; then printf '%s%%s\\n' %s; fi\n",
			shellQuote(worktree), strings.Join(specs, " "), matchPrefix, shellQuote(c.Name))
	}
	return sb.String()
}

// ParseMatchOutput reads MatchScript output into a set of matched names.
func ParseMatchOutput(out string) map[string]bool {
	matched := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if name, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), matchPrefix); ok && name != "" {
			matched[name] = true
		}
	}
	return matched
}

// RemoteSumsScript creates the user checklist directory, prints
// "==> home $HOME", then "SUM <sha256> <file name>" for each *.md in it.
func RemoteSumsScript() string {
	dir := `"$HOME"/` + shellQuote(UserChecklistDir)
	return fmt.Sprintf("mkdir -p %s || exit 1\n", dir) +
		fmt.Sprintf("printf '%s%s %%s\\n' \"$HOME\"\n", headerPrefix, homeSource) +
		fmt.Sprintf("for f in %s/*.md; do [ -f \"$f\" ] || continue; s=$(sha256sum < \"$f\") || continue; printf '%s%%s %%s\\n' \"${s%%%% *}\" \"${f##*/}\"; done\n", dir, sumPrefix)
}

// ParseRemoteSums reads RemoteSumsScript output: the remote $HOME and the
// sha256 of each checklist keyed by file name.
func ParseRemoteSums(out string) (home string, sums map[string]string) {
	sums = map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if h, ok := strings.CutPrefix(line, headerPrefix+homeSource+" "); ok {
			home = h
			continue
		}
		if rest, ok := strings.CutPrefix(line, sumPrefix); ok {
			sum, name, found := strings.Cut(rest, " ")
			if found && name != "" {
				sums[name] = sum
			}
		}
	}
	return home, sums
}

// PushPlan returns, sorted, the local file names whose sha256 differs from
// the remote copy or that are absent remotely.
func PushPlan(local map[string][]byte, remoteSums map[string]string) []string {
	var names []string
	for name, content := range local {
		h := sha256.Sum256(content)
		if remoteSums[name] != hex.EncodeToString(h[:]) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
