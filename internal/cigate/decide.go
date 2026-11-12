package cigate

import (
	"fmt"
	"strings"
)

// Kind is the question ci.gate answers.
type Kind string

const (
	// KindPush asks whether a git push of the current branch may go out.
	KindPush Kind = "push"
	// KindCreate asks whether gh pr create may run.
	KindCreate Kind = "create"
	// KindReady asks whether a draft PR may be marked ready.
	KindReady Kind = "ready"
)

// PR is what serve knows about the branch's pull request.
type PR struct {
	Number  int    `json:"number"`
	Draft   bool   `json:"draft"`
	State   string `json:"state"`
	HeadSHA string `json:"head_sha,omitempty"`
}

// Input is everything Decide needs. Record is the gate record for HEAD, nil
// when there is none.
type Input struct {
	Kind         Kind
	HasParent    bool
	ChildPRs     bool
	HasList      bool
	DraftsSkipCI bool
	Draft        bool
	PR           *PR
	LookupFailed bool
	Record       *Record
}

// ChildLandReason is the refusal for a child's PR create while children land
// locally.
const ChildLandReason = "aiman: children land with aiman land, not a PR"

// Decide answers a gate question. A refusal always carries a reason.
func Decide(in Input) (bool, string) {
	switch in.Kind {
	case KindPush:
		return decidePush(in)
	case KindCreate:
		return decideCreate(in)
	case KindReady:
		if !in.HasList {
			return true, ""
		}
		return needPass(in.Record)
	default:
		return false, fmt.Sprintf("unknown gate kind %q", in.Kind)
	}
}

func decidePush(in Input) (bool, string) {
	if !in.HasList {
		return true, ""
	}
	if in.LookupFailed {
		return needPass(in.Record)
	}
	if in.PR == nil || !strings.EqualFold(in.PR.State, "OPEN") {
		return true, ""
	}
	if in.PR.Draft && in.DraftsSkipCI {
		return true, ""
	}
	return needPass(in.Record)
}

func decideCreate(in Input) (bool, string) {
	if in.HasParent && !in.ChildPRs {
		return false, ChildLandReason
	}
	if in.DraftsSkipCI {
		if in.Draft {
			return true, ""
		}
		return false, "aiman: this repo skips CI on draft PRs: open it with gh pr create --draft, then aiman pr ready"
	}
	if !in.HasList {
		return true, ""
	}
	return needPass(in.Record)
}

func needPass(r *Record) (bool, string) {
	switch {
	case r == nil:
		return false, "aiman: the local CI gate has not run for HEAD: commit, run aiman ci local --wait, then retry"
	case r.Status == StatusPass:
		return true, ""
	case r.Status == StatusRunning:
		return false, "aiman: the local CI gate is still running for HEAD: wait for aiman ci status to pass"
	default:
		return false, fmt.Sprintf("aiman: the local CI gate failed for HEAD at %q: fix it, commit, run aiman ci local --wait, then retry", r.Failed)
	}
}

// PushRefusal is the reason given for a push command PushCommandOK refuses.
const PushRefusal = "aiman: push the current branch in its own command after committing"

// PushCommandOK reports whether cmd is a plain push of branch: git push,
// git push origin, git push -u origin HEAD or git push origin <branch>,
// with -u and -q allowed.
// Compound commands, other refspecs and other flags are refused.
func PushCommandOK(cmd, branch string) bool {
	if strings.ContainsAny(cmd, ";&|`$<>()\n\r") {
		return false
	}
	f := strings.Fields(cmd)
	if len(f) < 2 || f[0] != "git" || f[1] != "push" {
		return false
	}
	var args []string
	for _, a := range f[2:] {
		switch {
		case a == "-u" || a == "--set-upstream" || a == "-q" || a == "--quiet":
		case strings.HasPrefix(a, "-"):
			return false
		default:
			args = append(args, a)
		}
	}
	switch len(args) {
	case 0:
		return true
	case 1:
		return args[0] == "origin"
	case 2:
		return args[0] == "origin" && (args[1] == "HEAD" || (branch != "" && args[1] == branch))
	default:
		return false
	}
}

// TagPushName returns the tag cmd pushes when it is git push [-q] origin
// <tag> or git push [-q] origin refs/tags/<tag>. The caller still checks
// that the name is an existing tag and not a branch: the shape alone does
// not tell them apart.
func TagPushName(cmd string) (string, bool) {
	if strings.ContainsAny(cmd, ";&|`$<>()\n\r") {
		return "", false
	}
	f := strings.Fields(cmd)
	if len(f) < 2 || f[0] != "git" || f[1] != "push" {
		return "", false
	}
	var args []string
	for _, a := range f[2:] {
		switch {
		case a == "-q" || a == "--quiet":
		case strings.HasPrefix(a, "-"):
			return "", false
		default:
			args = append(args, a)
		}
	}
	if len(args) != 2 || args[0] != "origin" {
		return "", false
	}
	name := strings.TrimPrefix(args[1], "refs/tags/")
	if name == "" || strings.ContainsAny(name, ":^~*?[\\") || name == "HEAD" {
		return "", false
	}
	return name, true
}
