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
func (f File) Missing() []string {
	var out []string
	for _, q := range f.Questions {
		if q.Answer == "" {
			out = append(out, fmt.Sprintf("question %q has no answer", q.Text))
		}
	}
	for _, fd := range f.Findings {
		out = append(out, fd.missing()...)
	}
	if f.Summary == "" {
		out = append(out, "summary is empty")
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

// Post builds the review to send: the summary as the body and one inline
// comment per agreed finding, each prefixed with its label. Approval is never
// posted, and an incomplete file is refused.
func (f File) Post(event, commit string, single bool) (Post, error) {
	switch event {
	case "COMMENT", "REQUEST_CHANGES":
	case "APPROVE":
		return Post{}, errors.New("a review is never posted as APPROVE")
	default:
		return Post{}, fmt.Errorf("review event %q: want COMMENT or REQUEST_CHANGES", event)
	}
	if m := f.Missing(); len(m) > 0 {
		return Post{}, fmt.Errorf("review is incomplete: %s", strings.Join(m, "; "))
	}
	p := Post{Event: event, CommitID: commit, Body: f.Summary}
	if single {
		if p.Body != "" {
			p.Body += "\n\n"
		}
		p.Body += SingleReviewerNote
	}
	for _, fd := range f.Findings {
		if fd.Call != "agree" {
			continue
		}
		p.Comments = append(p.Comments, Comment{Path: fd.Path, Line: fd.Line, Body: fd.Label + ": " + fd.Body})
	}
	return p, nil
}
