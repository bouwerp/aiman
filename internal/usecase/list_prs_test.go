package usecase

import (
	"context"
	"strings"
	"testing"
)

func TestListOpenPRsNewestFirst(t *testing.T) {
	r := &createRemote{out: `[{"number":1,"title":"old","author":{"login":"ann"},"headRefName":"a","additions":1,"deletions":0},{"number":7,"title":"new","author":{"login":"bob"},"headRefName":"b","additions":12,"deletions":3}]`}
	prs, err := ListOpenPRs(context.Background(), r, "o/r")
	if err != nil {
		t.Fatalf("ListOpenPRs: %v", err)
	}
	if len(prs) != 2 {
		t.Fatalf("got %d PRs", len(prs))
	}
	if prs[0].Number != 7 || prs[0].Author != "bob" || prs[0].Additions != 12 {
		t.Fatalf("newest first: %+v", prs[0])
	}
	if prs[1].Number != 1 {
		t.Fatalf("older second: %+v", prs[1])
	}
	cmd := strings.Join(r.cmds, "\n")
	for _, want := range []string{"gh", "pr", "list", "--repo", "o/r", "--state", "open"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command missing %q: %s", want, cmd)
		}
	}
}
