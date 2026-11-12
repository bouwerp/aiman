package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

const landReport = "## Evidence\n- `go test ./...` exit 0\n\n## Surprises\nnone\n"

type landRig struct {
	srv                   *Server
	notes                 *fakeSteerStore
	origin, parent, child string
}

// newLandRig makes a bare origin with main and feat, a main clone, the
// coordinator p on feat and its child c on feat-t1 with one commit and a
// valid report.
func newLandRig(t *testing.T) landRig {
	t.Helper()
	return newLandRigWith(t, nil)
}

// newLandRigWith is newLandRig with edit applied to p and c before serve
// stores them.
func newLandRigWith(t *testing.T, edit func(p, c *domain.Session)) landRig {
	t.Helper()
	root := t.TempDir()
	r := landRig{origin: filepath.Join(root, "origin.git"), parent: filepath.Join(root, "r@feat"), child: filepath.Join(root, "r@feat-t1")}
	clone := filepath.Join(root, "r")
	gitIn(t, root, "init", "-q", "--bare", "-b", "main", r.origin)
	gitIn(t, root, "clone", "-q", r.origin, clone)
	gitIn(t, clone, "config", "user.name", "Aiman Test")
	gitIn(t, clone, "config", "user.email", "aiman@example.com")
	commitFile(t, clone, "a.txt", "base\n")
	gitIn(t, clone, "push", "-q", "origin", "HEAD:main", "HEAD:refs/heads/feat")
	gitIn(t, clone, "fetch", "-q", "origin")
	gitIn(t, clone, "remote", "set-head", "origin", "main")
	gitIn(t, clone, "worktree", "add", "-q", "-b", "feat", r.parent, "origin/feat")
	gitIn(t, clone, "worktree", "add", "-q", "-b", "feat-t1", r.child, "origin/feat")
	commitFile(t, r.child, "b.txt", "child\n")
	if err := os.WriteFile(filepath.Join(r.child, domain.AimanReportFileName), []byte(landReport), 0o600); err != nil {
		t.Fatal(err)
	}
	p := todoSession("p")
	p.Branch, p.WorktreePath, p.WorkingDirectory, p.RepoName = "feat", r.parent, r.parent, "o/r"
	c := todoSession("c")
	c.Name, c.ParentID, c.Branch, c.WorktreePath, c.WorkingDirectory, c.RepoName = "api", "p", "feat-t1", r.child, r.child, "o/r"
	if edit != nil {
		edit(&p, &c)
	}
	r.srv, _, r.notes = todoServer(t, p, c)
	r.srv.SetLandDir(filepath.Join(root, "land"))
	return r
}

func (r landRig) originFeat(t *testing.T) string {
	t.Helper()
	return gitIn(t, r.origin, "rev-parse", "refs/heads/feat")
}

// steeredWith waits for a note to p holding want.
func (r landRig) steeredWith(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, n := range r.notes.records() {
			if n.SessionID == "p" && strings.Contains(n.Text, want) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no note to p containing %q; notes %+v", want, r.notes.records())
}

func TestSessionLandMergesAndRecords(t *testing.T) {
	r := newLandRig(t)
	var res struct {
		Type     string `json:"type"`
		Branch   string `json:"branch"`
		MergeSHA string `json:"merge_sha"`
	}
	rpcOK(t, r.srv, "c", "session.land", map[string]any{}, &res)
	if res.Type != "landed" || res.Branch != "feat" || res.MergeSHA != r.originFeat(t) {
		t.Fatalf("result = %+v, origin/feat = %s", res, r.originFeat(t))
	}
	for _, id := range []string{"c", "p"} {
		sha, err := r.srv.latestLanded(context.Background(), id)
		if id == "c" && (err != nil || sha != res.MergeSHA) {
			t.Fatalf("child ledger merge sha = %q, %v", sha, err)
		}
		if !ledgerHas(t, r.srv, id, domain.EventChildLanded) {
			t.Fatalf("no child.landed row on %s", id)
		}
	}
	r.steeredWith(t, "[aiman] child api landed on feat at ")
	p, _ := r.srv.findSession(context.Background(), "p")
	c, _ := r.srv.findSession(context.Background(), "c")
	if ok, err := r.srv.LandedLocal(context.Background(), p, c); err != nil || !ok {
		t.Fatalf("LandedLocal = %v, %v", ok, err)
	}
}

func TestSessionLandRefusals(t *testing.T) {
	cases := map[string]struct {
		caller string
		mutate func(t *testing.T, r landRig)
		code   string
	}{
		"not a child": {caller: "p", code: CodeInvalidParams},
		"no report": {caller: "c", code: CodeInvalidParams, mutate: func(t *testing.T, r landRig) {
			_ = os.Remove(filepath.Join(r.child, domain.AimanReportFileName))
		}},
		"untracked file": {caller: "c", code: CodeInvalidParams, mutate: func(t *testing.T, r landRig) {
			if err := os.WriteFile(filepath.Join(r.child, "stray.txt"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		"gate failed": {caller: "c", code: CodeInvalidParams, mutate: func(t *testing.T, r landRig) {
			r.srv.SetLandGate(fakeLandGate{applies: true})
		}},
		"parent diverged": {caller: "c", code: CodeLandParentDiverged, mutate: func(t *testing.T, r landRig) {
			commitFile(t, r.parent, "p.txt", "local\n")
		}},
		"conflict": {caller: "c", code: CodeLandConflict, mutate: func(t *testing.T, r landRig) {
			commitFile(t, r.parent, "b.txt", "parent\n")
			gitIn(t, r.parent, "push", "-q", "origin", "feat")
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newLandRig(t)
			if tc.mutate != nil {
				tc.mutate(t, r)
			}
			before := r.originFeat(t)
			resp := rpc(t, r.srv, tc.caller, "session.land", map[string]any{})
			if resp.Error == nil || resp.Error.Code != tc.code {
				t.Fatalf("resp = %+v, want %s", resp.Error, tc.code)
			}
			if after := r.originFeat(t); after != before {
				t.Fatal("origin/feat moved on a refusal")
			}
			switch tc.code {
			case CodeLandParentDiverged:
				r.steeredWith(t, "[aiman] child api is waiting to land on feat: put your worktree on that branch")
			case CodeLandConflict:
				raw, _ := json.Marshal(resp.Error.Data)
				if !strings.Contains(string(raw), `"files":["b.txt"]`) {
					t.Fatalf("conflict data = %s", raw)
				}
			}
		})
	}
}

func TestSessionLandRefusesTheDefaultBranch(t *testing.T) {
	r := newLandRig(t)
	p, _ := r.srv.findSession(context.Background(), "p")
	p.Branch = "main"
	if err := r.srv.repo.Save(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	resp := rpc(t, r.srv, "c", "session.land", map[string]any{})
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams || !strings.Contains(resp.Error.Message, "default branch") {
		t.Fatalf("resp = %+v", resp.Error)
	}
}

type fakeLandGate struct{ applies, passed bool }

func (g fakeLandGate) PassedFor(context.Context, domain.Session, string) (bool, bool, error) {
	return g.applies, g.passed, nil
}

func ledgerHas(t *testing.T, srv *Server, id string, kind domain.EventKind) bool {
	t.Helper()
	events, err := srv.repo.(sessionEventLister).ListSessionEvents(context.Background(), id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

// A coordinator whose session branch is not the one its children were cut
// from (it moved to a per-project base) lands them on the child's base.
func TestSessionLandTargetsTheChildsBaseBranch(t *testing.T) {
	r := newLandRigWith(t, func(p, c *domain.Session) {
		p.Branch = "coordinator-home"
		c.BaseBranch = "feat"
	})
	var res struct {
		Branch   string `json:"branch"`
		MergeSHA string `json:"merge_sha"`
	}
	rpcOK(t, r.srv, "c", "session.land", map[string]any{}, &res)
	if res.Branch != "feat" || res.MergeSHA != r.originFeat(t) {
		t.Fatalf("result = %+v, origin/feat = %s", res, r.originFeat(t))
	}
	r.steeredWith(t, "[aiman] child api landed on feat at ")
	p, _ := r.srv.findSession(context.Background(), "p")
	c, _ := r.srv.findSession(context.Background(), "c")
	if ok, err := r.srv.LandedLocal(context.Background(), p, c); err != nil || !ok {
		t.Fatalf("LandedLocal = %v, %v", ok, err)
	}
}
