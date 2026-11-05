package review

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

const fixedSample = `# Review: o/r#7

## Findings

### F1 blocking a/b.go:4
Verified: yes (test fails)
Source: primary
Fixed: def

Nil deref.

### F2 blocking a/c.go:9
Verified: yes (test fails)
Source: both
Call: agree "real"

Race.

### F3 blocking a/d.go:2
Verified: yes (read it)
Source: primary
Call: disagree "intended"

Odd.

### F4 nit a/e.go:1
Verified: no
Source: primary
Fixed: bogus

Name.

## Summary

Fine otherwise.
`

func TestFixedLineParses(t *testing.T) {
	f, err := ParseFile(fixedSample)
	if err != nil {
		t.Fatal(err)
	}
	if f.Findings[0].Fixed != "def" || f.Findings[0].Body != "Nil deref." {
		t.Fatalf("finding: %+v", f.Findings[0])
	}
}

func TestMissingWithHonoursOnlyRecordedFixes(t *testing.T) {
	f, err := ParseFile(fixedSample)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.MissingWith(map[string]string{"F1": "def"}), "\n")
	if strings.Contains(joined, "F1:") {
		t.Fatalf("recorded fix still missing something: %s", joined)
	}
	if !strings.Contains(joined, "F4:") {
		t.Fatalf("unrecorded Fixed: line must count as an ordinary finding: %s", joined)
	}
	if nilJoined := strings.Join(f.Missing(), "\n"); !strings.Contains(nilJoined, "F1:") {
		t.Fatalf("Missing must not trust a Fixed: line by itself: %s", nilJoined)
	}
	wrong := strings.Join(f.MissingWith(map[string]string{"F1": "other"}), "\n")
	if !strings.Contains(wrong, "F1:") {
		t.Fatalf("a different sha must not count: %s", wrong)
	}
}

func TestOpenBlocking(t *testing.T) {
	f, err := ParseFile(fixedSample)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.OpenBlocking(map[string]string{"F1": "def"}); !slices.Equal(got, []string{"F2"}) {
		t.Fatalf("recorded fix: %v", got)
	}
	if got := f.OpenBlocking(nil); !slices.Equal(got, []string{"F1", "F2"}) {
		t.Fatalf("unrecorded fix stays open: %v", got)
	}
}

const approvable = `# Review: o/r#7

## Findings

### F1 blocking a/b.go:4
Verified: yes (test fails)
Source: primary
Fixed: def

Nil deref.

### F2 suggestion a/c.go:9
Verified: yes (read it)
Source: both
Call: agree "ok"

Simplify.

## Summary

Good.
`

func TestPostFixedListsFixesInsteadOfCommenting(t *testing.T) {
	f, err := ParseFile(approvable)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.PostFixed("COMMENT", "abc", false, map[string]string{"F1": "def"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Comments) != 1 || p.Comments[0].Path != "a/c.go" {
		t.Fatalf("comments: %+v", p.Comments)
	}
	if !strings.Contains(p.Body, "Fixed in def: F1 a/b.go") {
		t.Fatalf("body: %s", p.Body)
	}
	if _, err := f.PostFixed("APPROVE", "abc", false, map[string]string{"F1": "def"}); err == nil {
		t.Fatal("PostFixed must refuse APPROVE")
	}
	if _, err := f.Post("APPROVE", "abc", false); err == nil {
		t.Fatal("Post must refuse APPROVE")
	}
}

func TestApproval(t *testing.T) {
	f, err := ParseFile(approvable)
	if err != nil {
		t.Fatal(err)
	}
	fixed := map[string]string{"F1": "def"}
	p, err := f.Approval("def", true, fixed, false, "Assessed by an agent.")
	if err != nil {
		t.Fatal(err)
	}
	if p.Event != "APPROVE" || p.CommitID != "def" || len(p.Comments) != 1 {
		t.Fatalf("post: %+v", p)
	}
	for _, want := range []string{"Good.", SingleReviewerNote, "Fixed in def: F1", "Assessed by an agent."} {
		if !strings.Contains(p.Body, want) {
			t.Fatalf("body lacks %q: %s", want, p.Body)
		}
	}
	if p, err = f.Approval("def", false, fixed, true, "n"); err != nil || len(p.Comments) != 0 {
		t.Fatalf("already posted: %+v, %v", p, err)
	}
	if _, err := f.Approval("def", false, nil, false, "n"); err == nil {
		t.Fatal("an unrecorded fix leaves a blocking finding open: approval must be refused")
	}
}

func TestGHFixAndApproveCalls(t *testing.T) {
	ctx := context.Background()
	ref := Ref{Owner: "o", Repo: "r", Number: 7}
	var calls []call

	pr, err := fakeGH(`{"number":7,"headRefOid":"abc","isCrossRepository":true}`, &calls).PR(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !pr.Fork || !strings.Contains(strings.Join(calls[0].args, " "), "isCrossRepository") {
		t.Fatalf("fork: %+v, args %v", pr, calls[0].args)
	}

	calls = nil
	login, err := fakeGH("alice\n", &calls).Viewer(ctx)
	if err != nil || login != "alice" {
		t.Fatalf("viewer: %q, %v", login, err)
	}
	if !slices.Equal(calls[0].args, []string{"api", "user", "-q", ".login"}) {
		t.Fatalf("viewer args: %v", calls[0].args)
	}

	calls = nil
	dec, err := fakeGH(`{"reviewDecision":"REVIEW_REQUIRED"}`, &calls).ReviewDecision(ctx, ref)
	if err != nil || dec != "REVIEW_REQUIRED" {
		t.Fatalf("decision: %q, %v", dec, err)
	}
	if !slices.Equal(calls[0].args, []string{"pr", "view", "7", "--repo", "o/r", "--json", "reviewDecision"}) {
		t.Fatalf("decision args: %v", calls[0].args)
	}
}

func TestPostReviewOwnPR(t *testing.T) {
	gh := &GHCLI{RunInput: func(context.Context, string, string, ...string) (string, error) {
		return "", errors.New("gh: Unprocessable Entity (HTTP 422): Can not approve your own pull request")
	}}
	err := gh.PostReview(context.Background(), Ref{Owner: "o", Repo: "r", Number: 7}, Post{Event: "APPROVE"})
	if !errors.Is(err, ErrOwnPR) {
		t.Fatalf("want ErrOwnPR, got %v", err)
	}
}

func TestStateFixedMapLaterRoundWins(t *testing.T) {
	st := State{Fixes: []FixRound{
		{Fixed: []string{"F1", "F2"}, Findings: []string{"F1", "F2", "F3"}, PushedSHA: "aaa"},
		{Fixed: []string{"F1"}, Findings: []string{"F1"}, PushedSHA: "bbb"},
		{Fixed: []string{"F9"}, Findings: []string{"F9"}},
	}}
	got := st.FixedMap()
	if got["F1"] != "bbb" || got["F2"] != "aaa" {
		t.Fatalf("map: %v", got)
	}
	if _, ok := got["F3"]; ok {
		t.Fatal("a finding the round did not fix must not appear")
	}
	if _, ok := got["F9"]; ok {
		t.Fatal("a round that never pushed must not count")
	}
}

func TestStateRoundTripsNewFields(t *testing.T) {
	dir := t.TempDir()
	in := State{
		Ref: "o/r#7", HeadSHA: "abc",
		Fixes:        []FixRound{{Fixer: "x", Findings: []string{"F1"}, Fixed: []string{"F1"}, StartSHA: "abc", PushedSHA: "def"}},
		Assessment:   &AssessmentState{Consult: "c1", HeadSHA: "def"},
		ApproveGate:  &AssessmentState{Consult: "c1", HeadSHA: "def"},
		ApprovedHead: "def",
	}
	if err := SaveState(dir, "id", in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadState(dir, "id")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Fixes) != 1 || out.Fixes[0].PushedSHA != "def" || out.Assessment.Consult != "c1" || out.ApproveGate.HeadSHA != "def" || out.ApprovedHead != "def" {
		t.Fatalf("state: %+v", out)
	}
}

func TestParseFixReport(t *testing.T) {
	r, err := ParseFixReport("## Findings\n\nF1 fixed go test ./a passes\nF3 not fixed needs a product call\n\n## Surprises\n\nnone\n")
	if err != nil {
		t.Fatal(err)
	}
	if r.Fixed["F1"] != "go test ./a passes" || r.NotFixed["F3"] != "needs a product call" || r.Surprises != "none" {
		t.Fatalf("report: %+v", r)
	}
	for name, md := range map[string]string{
		"no surprises":   "## Findings\n\nF1 fixed x\n",
		"no findings":    "## Surprises\n\nnone\n",
		"empty evidence": "## Findings\n\nF1 fixed\n\n## Surprises\n\nnone\n",
		"empty reason":   "## Findings\n\nF1 not fixed\n\n## Surprises\n\nnone\n",
	} {
		if _, err := ParseFixReport(md); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
