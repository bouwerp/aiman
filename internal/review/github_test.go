package review

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type call struct {
	name  string
	args  []string
	input string
}

func fakeGH(out string, calls *[]call) *GHCLI {
	return &GHCLI{
		Run: func(_ context.Context, name string, args ...string) (string, error) {
			*calls = append(*calls, call{name: name, args: args})
			return out, nil
		},
		RunInput: func(_ context.Context, input, name string, args ...string) (string, error) {
			*calls = append(*calls, call{name: name, args: args, input: input})
			return "", nil
		},
	}
}

func TestGHCLIArgs(t *testing.T) {
	ctx := context.Background()
	var calls []call

	gh := fakeGH(`[{"number":7,"title":"T","author":{"login":"bob"},"headRefName":"feat","additions":3,"deletions":1}]`, &calls)
	prs, err := gh.ListPRs(ctx, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pr", "list", "--repo", "o/r", "--state", "open", "--json", "number,title,author,headRefName,additions,deletions"}
	if calls[0].name != "gh" || !reflect.DeepEqual(calls[0].args, want) {
		t.Fatalf("list args: %v", calls[0].args)
	}
	if len(prs) != 1 || prs[0].Author != "bob" || prs[0].HeadRef != "feat" || prs[0].Additions != 3 {
		t.Fatalf("prs: %+v", prs)
	}

	calls = nil
	gh = fakeGH(`{"number":7,"title":"T","author":{"login":"bob"},"body":"B","baseRefName":"main","headRefName":"feat","headRefOid":"abc123","additions":3,"deletions":1}`, &calls)
	pr, err := gh.PR(ctx, Ref{Owner: "o", Repo: "r", Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"pr", "view", "7", "--repo", "o/r", "--json", "number,title,author,body,baseRefName,headRefName,headRefOid,additions,deletions"}
	if !reflect.DeepEqual(calls[0].args, want) {
		t.Fatalf("view args: %v", calls[0].args)
	}
	if pr.HeadSHA != "abc123" || pr.BaseRef != "main" || pr.Body != "B" || pr.Deletions != 1 {
		t.Fatalf("pr: %+v", pr)
	}

	calls = nil
	post := Post{Event: "COMMENT", Body: "sum", CommitID: "abc123", Comments: []Comment{{Path: "a.go", Line: 4, Body: "nit: x"}}}
	if err := gh.PostReview(ctx, Ref{Owner: "o", Repo: "r", Number: 7}, post); err != nil {
		t.Fatal(err)
	}
	want = []string{"api", "--method", "POST", "repos/o/r/pulls/7/reviews", "--input", "-"}
	if !reflect.DeepEqual(calls[0].args, want) {
		t.Fatalf("post args: %v", calls[0].args)
	}
	var body struct {
		CommitID string `json:"commit_id"`
		Body     string `json:"body"`
		Event    string `json:"event"`
		Comments []struct {
			Path string `json:"path"`
			Line int    `json:"line"`
			Body string `json:"body"`
			Side string `json:"side"`
		} `json:"comments"`
	}
	if err := json.Unmarshal([]byte(calls[0].input), &body); err != nil {
		t.Fatal(err)
	}
	if body.CommitID != "abc123" || body.Event != "COMMENT" || body.Body != "sum" || len(body.Comments) != 1 ||
		body.Comments[0].Path != "a.go" || body.Comments[0].Line != 4 || body.Comments[0].Side != "RIGHT" {
		t.Fatalf("body: %s", calls[0].input)
	}
}

func TestPostReviewNeedsInputRunner(t *testing.T) {
	gh := &GHCLI{Run: func(context.Context, string, ...string) (string, error) { return "", nil }}
	err := gh.PostReview(context.Background(), Ref{Owner: "o", Repo: "r", Number: 1}, Post{Event: "COMMENT"})
	if err == nil || !strings.Contains(err.Error(), "RunInput") {
		t.Fatalf("err = %v", err)
	}
}
