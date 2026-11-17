package images_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness/images"
	"github.com/bouwerp/aiman/internal/infra/config"
)

const (
	cleanReport      = "## Findings\n- none\n## Verdict\nclean, nothing stands out.\n"
	suspiciousReport = "## Findings\n- [high] entry.sh pipes curl into sh\n## Verdict\nsuspicious: it downloads code at start.\n"
	maliciousReport  = "## Findings\n- [high] a miner\n## Verdict\nmalicious: it mines.\n"
)

// fakeRunner answers each container run with the next output.
type fakeRunner struct {
	mu   sync.Mutex
	outs []string
	runs []images.ReviewContainer
}

func (f *fakeRunner) Run(_ context.Context, c images.ReviewContainer) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, c)
	if len(f.outs) == 0 {
		return "", errors.New("no output scripted")
	}
	out := f.outs[0]
	if len(f.outs) > 1 {
		f.outs = f.outs[1:]
	}
	return out, nil
}

// fakeAgents is a host with claude and codex installed as ELF binaries.
type fakeAgents struct {
	dir   string
	creds map[string]images.Credential
	asked []string
}

func newFakeAgents(t *testing.T) *fakeAgents {
	t.Helper()
	a := &fakeAgents{dir: t.TempDir(), creds: map[string]images.Credential{}}
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(a.dir, name), []byte("\x7fELF fake"), 0o755); err != nil { //nolint:gosec // G306: a fake executable
			t.Fatal(err)
		}
		a.creds[name] = images.Credential{Mode: images.CredentialOAuthCopy, File: []byte(`{"token":"t"}`)}
	}
	return a
}

func (a *fakeAgents) lookPath(cmd string) (string, error) {
	p := filepath.Join(a.dir, cmd)
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

func (a *fakeAgents) credential(agentName string, _ time.Duration) (images.Credential, error) {
	a.asked = append(a.asked, agentName)
	c, ok := a.creds[agentName]
	if !ok {
		return images.Credential{}, errors.New("no credentials")
	}
	return c, nil
}

type reviewFixture struct {
	*vetFixture
	runner *fakeRunner
	agents *fakeAgents
}

// newReviewFixture is a vetter whose reviewer is the model reviewer with a
// fake container runner; the session s1 runs claude.
func newReviewFixture(t *testing.T, smart []config.PreferredAgent, outs ...string) *reviewFixture {
	t.Helper()
	r := &reviewFixture{runner: &fakeRunner{outs: outs}, agents: newFakeAgents(t)}
	r.vetFixture = newVetFixture(t, func(o *images.VetterOptions) {
		rv, err := images.NewModelReviewer(images.ReviewOptions{
			Dir:          o.Dir,
			Smart:        func() []config.PreferredAgent { return smart },
			LookPath:     r.agents.lookPath,
			SessionAgent: func(_ context.Context, id string) (string, string, bool) { return "claude", "opus", id == "s1" },
			Credentials:  r.agents.credential,
			Runner:       r.runner,
			Image:        func(context.Context) (string, error) { return "scanner:1", nil },
			AimanBin:     "/usr/local/bin/aiman",
		})
		if err != nil {
			t.Fatal(err)
		}
		o.Reviewer = rv
	})
	return r
}

var bothFamilies = []config.PreferredAgent{{Agent: "claude", Model: "opus"}, {Agent: "codex", Model: "gpt-5.6-sol"}}

func TestReviewPicksOtherFamily(t *testing.T) {
	f := newReviewFixture(t, bothFamilies, cleanReport)
	img := f.push("redis:7", "a")
	_, _ = f.ensure(t, "redis:7", "s1", false)
	rec := f.record(t, img)
	if rec.Scan.ReviewAgent != "codex" || rec.Scan.Independent == nil || !*rec.Scan.Independent {
		t.Fatalf("scan = %+v, want codex and independent", rec.Scan)
	}
	if len(f.runner.runs) != 1 {
		t.Fatalf("runs = %d", len(f.runner.runs))
	}
	c := f.runner.runs[0]
	if !slices.Equal(c.Allow, []string{"chatgpt.com:443"}) {
		t.Fatalf("allowlist = %v, want only the provider's API", c.Allow)
	}
	if !slices.Contains(c.Entrypoint, "gpt-5.6-sol") || !strings.Contains(strings.Join(c.Entrypoint, " "), "codex exec") {
		t.Fatalf("entrypoint = %v", c.Entrypoint)
	}
	for _, m := range c.Mounts {
		if !m.ReadOnly {
			t.Errorf("mount %s -> %s is writable", m.Host, m.Container)
		}
	}
	if !strings.Contains(c.Stdin, "hostile data") {
		t.Errorf("the brief does not call the bundle hostile data")
	}
	if _, err := os.Stat(rec.Scan.ReviewReport); err != nil {
		t.Fatalf("review report not kept: %v", err)
	}
	if filepath.Dir(rec.Scan.ReviewReport) != filepath.Dir(f.v.ReportPath(img.Digest())) {
		t.Errorf("review report %s is not next to the scan report", rec.Scan.ReviewReport)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "review", images.ShortDigest(img.Digest()))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the review scratch directory stayed")
	}
}

func TestNoOtherFamilyFallsBackNotIndependent(t *testing.T) {
	f := newReviewFixture(t, []config.PreferredAgent{{Agent: "claude", Model: "opus"}, {Agent: "claude", Model: "sonnet"}}, cleanReport)
	img := f.push("redis:7", "a")
	_, _ = f.ensure(t, "redis:7", "s1", false)
	rec := f.record(t, img)
	if rec.State != images.StatePendingApproval {
		t.Fatalf("state = %s (%v), want pending_approval", rec.State, rec.Reasons)
	}
	if rec.Scan.ReviewAgent != "claude" || rec.Scan.ReviewModel != "sonnet" || rec.Scan.Independent == nil || *rec.Scan.Independent {
		t.Fatalf("scan = %+v, want claude sonnet, not independent", rec.Scan)
	}
	if !strings.Contains(images.Summary(rec), "not independent") {
		t.Fatalf("summary %q does not say the review is not independent", images.Summary(rec))
	}
	report, err := os.ReadFile(rec.Scan.Report)
	if err != nil || !strings.Contains(string(report), "independent: false") {
		t.Fatalf("scan report does not mark the review: %v\n%s", err, report)
	}
}

func TestMaliciousBlocks(t *testing.T) {
	f := newReviewFixture(t, bothFamilies, maliciousReport)
	img := f.push("redis:7", "a")
	_, _ = f.ensure(t, "redis:7", "", true)
	rec := f.record(t, img)
	if rec.State != images.StateBlocked || !slices.Contains(rec.Reasons, "image review verdict malicious") || f.loader.Loads != 0 {
		t.Fatalf("record = %s %v, loads %d; want blocked by the review", rec.State, rec.Reasons, f.loader.Loads)
	}
}

func TestSuspiciousAsks(t *testing.T) {
	f := newReviewFixture(t, bothFamilies, suspiciousReport)
	img := f.push("postgres:16", "a")
	_, _ = f.ensure(t, "postgres:16", "", true)
	rec := f.record(t, img)
	if rec.State != images.StatePendingApproval || f.loader.Loads != 0 {
		t.Fatalf("state = %s, loads %d; a suspicious verdict must ask, also for a listed image and the operator", rec.State, f.loader.Loads)
	}
	if s := images.Summary(rec); !strings.Contains(s, "review suspicious") || !strings.Contains(s, "1 high") {
		t.Fatalf("summary %q does not list the suspicious review", s)
	}
}

func TestCleanVerdictNeverClearsHit(t *testing.T) {
	f := newReviewFixture(t, bothFamilies, cleanReport)
	f.scanner.Set(images.ScanStageStatic, images.StageResult{StaticHits: []string{"clamav:Eicar /0/x"}})
	img := f.push("postgres:16", "a")
	_, _ = f.ensure(t, "postgres:16", "", true)
	if rec := f.record(t, img); rec.State != images.StateBlocked || rec.Scan.ModelVerdict != images.VerdictClean {
		t.Fatalf("static hit with a clean verdict: %s (%s)", rec.State, rec.Scan.ModelVerdict)
	}

	g := newReviewFixture(t, bothFamilies, cleanReport)
	g.scanner.Set(images.ScanStageCVE, images.StageResult{High: []string{"CVE-2026-2"}})
	img2 := g.push("postgres:16", "b")
	_, _ = g.ensure(t, "postgres:16", "", true)
	if rec := g.record(t, img2); rec.State != images.StatePendingApproval {
		t.Fatalf("high CVE with a clean verdict: %s, want pending_approval", rec.State)
	}
}

func TestUnparseableReportFailsNeverClean(t *testing.T) {
	f := newReviewFixture(t, bothFamilies, "I think it is fine.")
	img := f.push("postgres:16", "a")
	_, _ = f.ensure(t, "postgres:16", "", true)
	rec := f.record(t, img)
	if rec.State != images.StateFailed || rec.Scan.ModelVerdict != "" {
		t.Fatalf("unparseable report: %s %q, want failed", rec.State, rec.Scan.ModelVerdict)
	}
	if len(f.runner.runs) != 2 || !strings.Contains(f.runner.runs[1].Stdin, "could not be read") {
		t.Fatalf("runs = %d; want one request to fix the report", len(f.runner.runs))
	}
}

func TestReviewRetryFixesReport(t *testing.T) {
	f := newReviewFixture(t, bothFamilies, "no headings", cleanReport)
	img := f.push("postgres:16", "a")
	_, _ = f.ensure(t, "postgres:16", "", true)
	if rec := f.record(t, img); rec.State != images.StateApproved || rec.Scan.ModelVerdict != images.VerdictClean {
		t.Fatalf("after a fixed report: %s %q", rec.State, rec.Scan.ModelVerdict)
	}
}

func TestNoReviewAgentIsRetryableFailure(t *testing.T) {
	f := newReviewFixture(t, []config.PreferredAgent{{Agent: "grok", Model: "grok-4.6"}}, cleanReport)
	img := f.push("postgres:16", "a")
	_, _ = f.ensure(t, "postgres:16", "s1", false)
	rec := f.record(t, img)
	if rec.State != images.StateFailed || !rec.Retryable || len(f.runner.runs) != 0 {
		t.Fatalf("record = %s retryable=%v runs=%d, want a retryable failure and no run", rec.State, rec.Retryable, len(f.runner.runs))
	}
}
