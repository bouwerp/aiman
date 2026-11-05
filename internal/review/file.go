package review

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// SingleReviewerNote is added to the review body when only one agent reviewed.
const SingleReviewerNote = "Reviewed by one agent; the second reviewer was unavailable."

// Finding is one entry of the Findings section.
type Finding struct {
	ID       string
	Label    string // blocking, suggestion, question or nit
	Path     string
	Line     int
	Verified bool
	Evidence string
	Source   string // primary, secondary or both
	Call     string // agree or disagree; empty until the operator decides
	Quote    string // the operator's words behind the call
	Fixed    string // sha of the commit that fixed it; counts only when State.Fixes records it
	Body     string
}

// Question is one point the agent could not settle and asked the operator.
type Question struct {
	Text   string
	Answer string // the operator's words
}

// File is a parsed review file.
type File struct {
	Questions []Question
	Findings  []Finding
	Summary   string
}

var (
	findingHead = regexp.MustCompile(`^###\s+(\S+)\s+(\S+)(?:\s+(.+?)(?::(\d+))?)?\s*$`)
	verifiedRe  = regexp.MustCompile(`^Verified:\s*(yes|no)\b\s*(?:\((.*)\))?\s*$`)
	fixedRe     = regexp.MustCompile(`^Fixed:\s*(\S+)\s*$`)
	callRe      = regexp.MustCompile(`^Call:\s*(agree|disagree)\b\s*(?:"(.*)")?\s*$`)
	labels      = map[string]bool{"blocking": true, "suggestion": true, "question": true, "nit": true}
)

// ParseFile parses the review file format. It checks the structure of each
// finding but leaves completeness to Missing.
func ParseFile(md string) (File, error) {
	var f File
	var section string // "questions", "findings", "summary"
	var question string
	var answerLines, summary []string
	var cur *Finding
	var bodyLines []string

	flushAnswer := func() {
		if question != "" {
			f.Questions = append(f.Questions, Question{Text: question, Answer: strings.TrimSpace(strings.Join(answerLines, "\n"))})
		}
		question, answerLines = "", nil
	}
	flushFinding := func() {
		if cur != nil {
			cur.Body = strings.TrimSpace(strings.Join(bodyLines, "\n"))
			f.Findings = append(f.Findings, *cur)
		}
		cur, bodyLines = nil, nil
	}

	for _, line := range strings.Split(md, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if strings.HasPrefix(line, "## ") {
			flushAnswer()
			flushFinding()
			section = strings.ToLower(strings.TrimSpace(line[3:]))
			continue
		}
		switch section {
		case "questions", "answers":
			if strings.HasPrefix(line, "### ") {
				flushAnswer()
				question = strings.TrimSpace(line[4:])
			} else if question != "" {
				answerLines = append(answerLines, strings.TrimPrefix(strings.TrimPrefix(line, ">"), " "))
			}
		case "findings":
			if strings.HasPrefix(line, "### ") {
				flushFinding()
				fd, err := parseFindingHead(line)
				if err != nil {
					return File{}, err
				}
				cur = &fd
			} else if cur != nil {
				if !cur.takeField(line, len(bodyLines) == 0) {
					bodyLines = append(bodyLines, line)
				}
			}
		case "summary":
			summary = append(summary, line)
		}
	}
	flushAnswer()
	flushFinding()
	f.Summary = strings.TrimSpace(strings.Join(summary, "\n"))
	return f, nil
}

func parseFindingHead(line string) (Finding, error) {
	m := findingHead.FindStringSubmatch(line)
	if m == nil || !labels[m[2]] {
		return Finding{}, fmt.Errorf("finding heading %q: want \"### F1 <blocking|suggestion|question|nit> path:line\"", line)
	}
	fd := Finding{ID: m[1], Label: m[2], Path: m[3]}
	if m[4] != "" {
		fd.Line, _ = strconv.Atoi(m[4])
	}
	return fd, nil
}

// takeField consumes a Verified, Source or Call line that precedes the body.
func (fd *Finding) takeField(line string, beforeBody bool) bool {
	if !beforeBody {
		return false
	}
	if m := verifiedRe.FindStringSubmatch(line); m != nil {
		fd.Verified = m[1] == "yes"
		fd.Evidence = m[2]
		return true
	}
	if m := callRe.FindStringSubmatch(line); m != nil {
		fd.Call, fd.Quote = m[1], m[2]
		return true
	}
	if m := fixedRe.FindStringSubmatch(line); m != nil {
		fd.Fixed = m[1]
		return true
	}
	if v, ok := strings.CutPrefix(line, "Source:"); ok {
		fd.Source = strings.TrimSpace(v)
		return true
	}
	return strings.TrimSpace(line) == ""
}

// Missing lists what keeps the file from being posted: unanswered questions,
// findings without a call, and unverified findings that are not questions.
// A file with no questions is complete: the agent asks only what it could
// not settle from the code and the PR.
func (f File) Missing() []string { return f.MissingWith(nil) }

// MissingWith is Missing for a file whose findings may be fixed. fixed maps a
// finding id to the sha State.Fixes records; a finding whose Fixed line equals
// its entry needs no call or verification. Any other Fixed value is ignored.
func (f File) MissingWith(fixed map[string]string) []string {
	var out []string
	for _, q := range f.Questions {
		if q.Answer == "" {
			out = append(out, fmt.Sprintf("question %q has no answer", q.Text))
		}
	}
	for _, fd := range f.Findings {
		if fd.isFixed(fixed) {
			continue
		}
		out = append(out, fd.missing()...)
	}
	if f.Summary == "" {
		out = append(out, "summary is empty")
	}
	return out
}

// isFixed reports whether the finding's Fixed line matches a recorded fix.
func (fd Finding) isFixed(fixed map[string]string) bool {
	sha, ok := fixed[fd.ID]
	return ok && sha != "" && fd.Fixed == sha
}

// OpenBlocking lists the ids of blocking findings that are neither recorded as
// fixed nor answered with Call: disagree.
func (f File) OpenBlocking(fixed map[string]string) []string {
	var out []string
	for _, fd := range f.Findings {
		if fd.Label == "blocking" && !fd.isFixed(fixed) && fd.Call != "disagree" {
			out = append(out, fd.ID)
		}
	}
	return out
}

// missing lists what a finding still needs before it can be posted.
func (fd Finding) missing() []string {
	var out []string
	add := func(msg string) { out = append(out, fd.ID+": "+msg) }
	if fd.Call == "" {
		add("no call (agree or disagree)")
	} else if fd.Quote == "" {
		add("call needs the operator's quote")
	}
	if !fd.Verified && fd.Label != "question" {
		add(fmt.Sprintf("unverified %s finding (verify it or make it a question)", fd.Label))
	}
	if fd.Verified && fd.Evidence == "" {
		add("verified finding needs evidence")
	}
	if fd.Call == "agree" {
		if fd.Path == "" || fd.Line <= 0 {
			add("agreed finding needs path:line")
		}
		if fd.Body == "" {
			add("agreed finding needs a comment body")
		}
	}
	return out
}

// Post builds the review to send with no recorded fixes; see PostFixed.
func (f File) Post(event, commit string, single bool) (Post, error) {
	return f.PostFixed(event, commit, single, nil)
}

// PostFixed builds the review to send: the summary as the body and one inline
// comment per agreed finding, each prefixed with its label. Findings recorded
// as fixed get no comment; the body lists each as "Fixed in <sha>: <id> <path>".
// Approval is never posted here, and an incomplete file is refused.
func (f File) PostFixed(event, commit string, single bool, fixed map[string]string) (Post, error) {
	switch event {
	case "COMMENT", "REQUEST_CHANGES":
	case "APPROVE":
		return Post{}, errors.New("a review is never posted as APPROVE")
	default:
		return Post{}, fmt.Errorf("review event %q: want COMMENT or REQUEST_CHANGES", event)
	}
	return f.build(event, commit, single, fixed, false, "")
}

// Approval builds the APPROVE review. Incomplete files and open blocking
// findings are refused. The body is the summary, the single-reviewer note, the
// fixed lines and note; with posted true it carries no inline comments.
func (f File) Approval(commit string, single bool, fixed map[string]string, posted bool, note string) (Post, error) {
	if open := f.OpenBlocking(fixed); len(open) > 0 {
		return Post{}, fmt.Errorf("blocking findings are still open: %s", strings.Join(open, ", "))
	}
	return f.build("APPROVE", commit, single, fixed, posted, note)
}

func (f File) build(event, commit string, single bool, fixed map[string]string, noComments bool, note string) (Post, error) {
	if m := f.MissingWith(fixed); len(m) > 0 {
		return Post{}, fmt.Errorf("review is incomplete: %s", strings.Join(m, "; "))
	}
	p := Post{Event: event, CommitID: commit, Body: f.Summary}
	add := func(s string) {
		if s == "" {
			return
		}
		if p.Body != "" {
			p.Body += "\n\n"
		}
		p.Body += s
	}
	if single {
		add(SingleReviewerNote)
	}
	var lines []string
	for _, fd := range f.Findings {
		if fd.isFixed(fixed) {
			lines = append(lines, fmt.Sprintf("Fixed in %s: %s %s", fd.Fixed, fd.ID, fd.Path))
		}
	}
	add(strings.Join(lines, "\n"))
	add(strings.TrimSpace(note))
	if noComments {
		return p, nil
	}
	for _, fd := range f.Findings {
		if fd.Call != "agree" || fd.isFixed(fixed) {
			continue
		}
		p.Comments = append(p.Comments, Comment{Path: fd.Path, Line: fd.Line, Body: fd.Label + ": " + fd.Body})
	}
	return p, nil
}
