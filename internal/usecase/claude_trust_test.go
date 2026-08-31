package usecase

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMergeClaudeTrustedProjectsSetsHasTrustDialogAccepted(t *testing.T) {
	raw := []byte(`{
  "numStartups": 3,
  "projects": {
    "/old/path": {"hasTrustDialogAccepted": false, "allowedTools": ["Bash"]},
    "/keep/me": {"lastCost": 1.2}
  }
}`)
	got, err := mergeClaudeTrustedProjects(raw, []string{"/old/path", "/new/worktree", "  ", "/new/worktree"})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, got)
	}
	if doc["numStartups"].(float64) != 3 {
		t.Fatalf("top-level fields must be preserved, got %#v", doc["numStartups"])
	}
	projects := doc["projects"].(map[string]any)

	old := projects["/old/path"].(map[string]any)
	if old["hasTrustDialogAccepted"] != true {
		t.Fatalf("old path not trusted: %#v", old)
	}
	if tools, ok := old["allowedTools"].([]any); !ok || len(tools) != 1 {
		t.Fatalf("existing project fields must be preserved: %#v", old)
	}

	keep := projects["/keep/me"].(map[string]any)
	if _, ok := keep["hasTrustDialogAccepted"]; ok {
		t.Fatalf("untouched projects must stay untouched: %#v", keep)
	}

	neu := projects["/new/worktree"].(map[string]any)
	if neu["hasTrustDialogAccepted"] != true {
		t.Fatalf("new path not trusted: %#v", neu)
	}
}

func TestMergeClaudeTrustedProjectsStartsEmptyFile(t *testing.T) {
	got, err := mergeClaudeTrustedProjects(nil, []string{"/repos/app@branch"})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !strings.Contains(string(got), `"/repos/app@branch"`) {
		t.Fatalf("missing path in %s", got)
	}
	if !strings.Contains(string(got), `"hasTrustDialogAccepted": true`) {
		t.Fatalf("missing trust flag in %s", got)
	}
}

func TestMergeClaudeTrustedProjectsRejectsInvalidJSON(t *testing.T) {
	_, err := mergeClaudeTrustedProjects([]byte("{nope"), []string{"/x"})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// EnsureClaudeWorkspaceTrusted must write ~/.claude.json before any agent
// boots: `claude trust` is no longer a CLI subcommand, and trusting after
// launch leaves the dialog on screen (or lets a typed prompt kill the agent).
type claudeTrustRemote struct {
	home     string
	existing string
	cmds     []string
	written  map[string][]byte
}

func (r *claudeTrustRemote) Execute(_ context.Context, cmd string) (string, error) {
	r.cmds = append(r.cmds, cmd)
	switch {
	case strings.Contains(cmd, `printf %s "$HOME"`):
		return r.home, nil
	case strings.Contains(cmd, ".claude.json"):
		return r.existing, nil
	default:
		return "", nil
	}
}

func (r *claudeTrustRemote) WriteFile(_ context.Context, path string, content []byte) error {
	if r.written == nil {
		r.written = map[string][]byte{}
	}
	r.written[path] = content
	return nil
}

func TestEnsureClaudeWorkspaceTrustedWritesClaudeJSON(t *testing.T) {
	r := &claudeTrustRemote{home: "/home/code"}
	if err := EnsureClaudeWorkspaceTrusted(context.Background(), r, "/home/code/repos/app@feat", "/home/code/repos/app@feat/svc"); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	body, ok := r.written["/home/code/.claude.json"]
	if !ok {
		t.Fatalf("expected WriteFile to ~/.claude.json, wrote %#v; cmds=%v", r.written, r.cmds)
	}
	for _, want := range []string{
		`"/home/code/repos/app@feat"`,
		`"/home/code/repos/app@feat/svc"`,
		`"hasTrustDialogAccepted": true`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("claude.json missing %s:\n%s", want, body)
		}
	}
	joined := strings.Join(r.cmds, "\n")
	if strings.Contains(joined, "claude trust") {
		t.Fatalf("must not call removed `claude trust` subcommand, got:\n%s", joined)
	}
}

func TestEnsureClaudeWorkspaceTrustedMergesExistingFile(t *testing.T) {
	r := &claudeTrustRemote{
		home:     "/home/code",
		existing: `{"projects":{"/keep":{"allowedTools":[]}}}`,
	}
	if err := EnsureClaudeWorkspaceTrusted(context.Background(), r, "/new"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	body := string(r.written["/home/code/.claude.json"])
	if !strings.Contains(body, `"/keep"`) || !strings.Contains(body, `"/new"`) {
		t.Fatalf("merge lost projects: %s", body)
	}
}

func TestTrustWorkspaceBeforeLaunchDoesNotUseClaudeTrustCLI(t *testing.T) {
	r := &claudeTrustRemote{home: "/home/code"}
	fm := &FlowManager{}
	fm.trustWorkspaceBeforeLaunch(context.Background(), r, "/home/code/repos/app@feat", "/home/code/repos/app@feat")

	joined := strings.Join(r.cmds, "\n")
	if strings.Contains(joined, "claude trust") {
		t.Fatalf("pre-launch trust must not use removed claude trust CLI:\n%s", joined)
	}
	if !strings.Contains(joined, "safe.directory") {
		t.Fatalf("expected git safe.directory, got:\n%s", joined)
	}
	if _, ok := r.written["/home/code/.claude.json"]; !ok {
		t.Fatalf("expected ~/.claude.json write, wrote %#v", r.written)
	}
}
