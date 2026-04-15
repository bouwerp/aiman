package git

import (
	"context"
	"fmt"
	"strconv"
	"testing"
)

func TestParseGitHubOwnerRepo(t *testing.T) {
	tests := []struct {
		raw   string
		owner string
		repo  string
		ok    bool
	}{
		{"git@github.com:acme/widget.git", "acme", "widget", true},
		{"https://github.com/acme/widget", "acme", "widget", true},
		{"https://github.com/acme/widget.git", "acme", "widget", true},
		{"git@git.example.com:myorg/myrepo", "myorg", "myrepo", true},
		{"", "", "", false},
		{"not-a-url", "", "", false},
	}
	for _, tt := range tests {
		o, r, ok := parseGitHubOwnerRepo(tt.raw)
		if ok != tt.ok || o != tt.owner || r != tt.repo {
			t.Errorf("parseGitHubOwnerRepo(%q) = (%q,%q,%v) want (%q,%q,%v)", tt.raw, o, r, ok, tt.owner, tt.repo, tt.ok)
		}
	}
}

func TestDeriveDisplayState(t *testing.T) {
	if deriveDisplayState("OPEN", true, false) != "draft" {
		t.Fatal("draft")
	}
	if deriveDisplayState("OPEN", false, true) != "merged" {
		t.Fatal("merged")
	}
	if deriveDisplayState("CLOSED", false, false) != "closed" {
		t.Fatal("closed")
	}
	if deriveDisplayState("OPEN", false, false) != "open" {
		t.Fatal("open")
	}
}

func TestResolvePullRequestForBranch_headRefNameFallback(t *testing.T) {
	ctx := context.Background()
	repoPath := "/remote/wt"
	branch := "FeatureZ"
	owner, repo := "acme", "widget"
	repoSlug := owner + "/" + repo
	repoFlag := "--repo " + strconv.Quote(repoSlug)

	viewBody := `{"number":77,"title":"fix","state":"OPEN","isDraft":false,"mergedAt":"","url":"https://example/pr/77","reviewDecision":"","mergeable":"UNKNOWN","mergeStateStatus":"","reviews":[],"comments":[],"statusCheckRollup":[]}`

	cmdView0 := fmt.Sprintf("cd %q && gh pr view --json %s%s", repoPath, prViewJSONFields, ghJSONCmdSuffix)
	cmdViewRepo := fmt.Sprintf("cd %q && gh pr view %s --json %s%s", repoPath, repoFlag, prViewJSONFields, ghJSONCmdSuffix)
	cmdViewBranch := fmt.Sprintf("cd %q && gh pr view %s %s --json %s%s", repoPath, strconv.Quote(branch), repoFlag, prViewJSONFields, ghJSONCmdSuffix)
	head := owner + ":" + branch
	cmdListHead := fmt.Sprintf("cd %q && gh pr list %s --head %q --state all --limit 1 --json number%s",
		repoPath, repoFlag, head, ghJSONCmdSuffix)
	cmdScan := fmt.Sprintf("cd %q && gh pr list %s --state all --limit 50 --json number,headRefName,state%s",
		repoPath, repoFlag, ghJSONCmdSuffix)
	cmdViewNum := fmt.Sprintf("cd %q && gh pr view 77 --json %s%s", repoPath, prViewJSONFields, ghJSONCmdSuffix)

	r := &mockRemote{
		outputs: map[string]string{
			cmdView0:      "",
			cmdViewRepo:   "",
			cmdViewBranch: "",
			cmdListHead:   "[]",
			cmdScan:       `[{"number":77,"headRefName":"featurez","state":"OPEN"}]`,
			cmdViewNum:    viewBody,
		},
	}
	pr := resolvePullRequestForBranch(ctx, r, repoPath, owner, repo, true, branch)
	if pr == nil || pr.Number != 77 {
		t.Fatalf("got %#v, want PR #77", pr)
	}
}

func TestDomainViewToPullRequest_ChecksFromConclusion(t *testing.T) {
	v := &ghPRViewJSON{
		Number: 1,
		Title:  "test",
		State:  "OPEN",
		StatusCheckRollup: []struct {
			Context    string `json:"context"`
			State      string `json:"state"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		}{
			{Status: "COMPLETED", Conclusion: "SUCCESS"},
			{Status: "COMPLETED", Conclusion: "SUCCESS"},
			{Status: "IN_PROGRESS"},
		},
	}
	pr := domainViewToPullRequest(v)
	if pr == nil {
		t.Fatal("expected PR")
	}
	if pr.ChecksSummary != "2/3 passed" {
		t.Fatalf("got summary %q, want %q", pr.ChecksSummary, "2/3 passed")
	}
	if pr.ChecksStatus != "pending" {
		t.Fatalf("got status %q, want %q", pr.ChecksStatus, "pending")
	}
}
