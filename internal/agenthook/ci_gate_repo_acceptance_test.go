package agenthook

import "testing"

// Acceptance tests for multi-repo sessions, piece P3: the CI gate hook names
// the repository a gh call targets, so serve can judge it against that
// member of a workspace session. Children must not edit this file.

func TestGateQuestionGHRepo(t *testing.T) {
	cases := []struct {
		command string
		kind    string
		draft   bool
		repo    string
	}{
		{"gh -R o/b pr create", GateKindCreate, false, "o/b"},
		{"gh --repo o/b pr create --draft", GateKindCreate, true, "o/b"},
		{"gh -R o/b pr new -d --fill", GateKindCreate, true, "o/b"},
		{"gh -R o/b pr ready", GateKindReady, false, "o/b"},
		{"gh --repo o/a pr ready 12", GateKindReady, false, "o/a"},
		{"cd aiman-phone && gh -R bouwerp/aiman-phone pr create --base main", GateKindCreate, false, "bouwerp/aiman-phone"},
		{"gh pr create", GateKindCreate, false, ""},
		{"gh pr ready", GateKindReady, false, ""},
	}
	for _, c := range cases {
		q, ok := GateQuestionFor(hookPayload(c.command))
		if !ok {
			t.Fatalf("%q: not gated", c.command)
		}
		if q.Kind != c.kind || q.Draft != c.draft || q.Repo != c.repo || q.Command != c.command {
			t.Fatalf("%q: %+v, want kind %s draft %v repo %q and the command verbatim", c.command, q, c.kind, c.draft, c.repo)
		}
	}
}

// git -C pushes keep working; a push names no gh repository.
func TestGateQuestionGHRepoLeavesGitPush(t *testing.T) {
	for _, command := range []string{"git -C aiman-phone push", "git -C /w/host-tab@ws/aiman push -u origin host-tab", "git push"} {
		q, ok := GateQuestionFor(hookPayload(command))
		if !ok || q.Kind != GateKindPush || q.Command != command || q.Repo != "" {
			t.Fatalf("%q: %+v %v, want a push with no repo", command, q, ok)
		}
	}
}
