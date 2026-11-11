package cigate_test

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/cigate"
)

// ci-minutes rev 3: what ci.gate answers for push and PR create.

func pass() *cigate.Record { return &cigate.Record{Status: cigate.StatusPass} }

func TestDecidePush(t *testing.T) {
	open := &cigate.PR{Number: 7, State: "OPEN"}
	draft := &cigate.PR{Number: 7, State: "OPEN", Draft: true}
	cases := []struct {
		name  string
		in    cigate.Input
		allow bool
	}{
		{"no PR", cigate.Input{Kind: cigate.KindPush, HasList: true}, true},
		{"open PR, no record", cigate.Input{Kind: cigate.KindPush, HasList: true, PR: open}, false},
		{"open PR, failed record", cigate.Input{Kind: cigate.KindPush, HasList: true, PR: open, Record: &cigate.Record{Status: cigate.StatusFail}}, false},
		{"open PR, pass", cigate.Input{Kind: cigate.KindPush, HasList: true, PR: open, Record: pass()}, true},
		{"draft, repo skips drafts", cigate.Input{Kind: cigate.KindPush, HasList: true, PR: draft, DraftsSkipCI: true}, true},
		{"draft, repo runs drafts", cigate.Input{Kind: cigate.KindPush, HasList: true, PR: draft}, false},
		{"lookup failed with a list", cigate.Input{Kind: cigate.KindPush, HasList: true, LookupFailed: true}, false},
		{"no list", cigate.Input{Kind: cigate.KindPush, PR: open}, true},
		{"closed PR", cigate.Input{Kind: cigate.KindPush, HasList: true, PR: &cigate.PR{Number: 7, State: "MERGED"}}, true},
	}
	for _, c := range cases {
		if allow, reason := cigate.Decide(c.in); allow != c.allow {
			t.Errorf("%s: allow = %v (%s), want %v", c.name, allow, reason, c.allow)
		} else if !allow && reason == "" {
			t.Errorf("%s: a refusal must say why", c.name)
		}
	}
}

func TestDecideCreate(t *testing.T) {
	allow, reason := cigate.Decide(cigate.Input{Kind: cigate.KindCreate, HasParent: true, Draft: true, DraftsSkipCI: true})
	if allow || !strings.Contains(reason, "aiman land") {
		t.Fatalf("child create: %v %q, want refused pointing at aiman land", allow, reason)
	}
	if allow, _ := cigate.Decide(cigate.Input{Kind: cigate.KindCreate, HasParent: true, ChildPRs: true, Draft: true, DraftsSkipCI: true}); !allow {
		t.Fatal("child create with child_prs true and a draft should pass")
	}
	if allow, reason := cigate.Decide(cigate.Input{Kind: cigate.KindCreate, DraftsSkipCI: true, HasList: true}); allow || !strings.Contains(reason, "--draft") {
		t.Fatalf("non-draft create in a draft-skipping repo: %v %q, want refused naming --draft", allow, reason)
	}
	if allow, _ := cigate.Decide(cigate.Input{Kind: cigate.KindCreate, DraftsSkipCI: true, HasList: true, Draft: true}); !allow {
		t.Fatal("a draft create in a draft-skipping repo is free")
	}
	if allow, _ := cigate.Decide(cigate.Input{Kind: cigate.KindCreate, HasList: true, Draft: true}); allow {
		t.Fatal("a create in a repo that runs drafts needs a pass record")
	}
	if allow, _ := cigate.Decide(cigate.Input{Kind: cigate.KindCreate, HasList: true, Record: pass()}); !allow {
		t.Fatal("a create with a pass record should pass")
	}
}

func TestPushCommandShape(t *testing.T) {
	ok := []string{"git push", "git push origin", "git push -u origin HEAD", "git push origin feat-x"}
	bad := []string{"git push origin abc123:feat-x", "git push origin other", "git commit -am x && git push", "git push; ls", "git push | tee x", "git push --force"}
	for _, c := range ok {
		if !cigate.PushCommandOK(c, "feat-x") {
			t.Errorf("PushCommandOK(%q) = false, want true", c)
		}
	}
	for _, c := range bad {
		if cigate.PushCommandOK(c, "feat-x") {
			t.Errorf("PushCommandOK(%q) = true, want false", c)
		}
	}
}
