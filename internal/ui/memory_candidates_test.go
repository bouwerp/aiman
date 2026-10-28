package ui

import (
	"context"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/memoryjob"
)

func testMemoryModel() MemoryCandidatesModel {
	return NewMemoryCandidatesModel(nil, nil)
}

func addTestProposal(t *testing.T, m MemoryCandidatesModel) MemoryCandidatesModel {
	t.Helper()
	model, _ := m.Update(memoryProposalMsg{
		host: "h1",
		job:  memoryjob.Job{ID: "j1", Label: "PROJ-1"},
		candidates: []domain.MemoryCandidate{
			{Type: domain.ContextTypeDecision, Title: "One", Abstract: "a", Body: "b"},
			{Type: domain.ContextTypeFact, Title: "Two", Abstract: "a", Body: "b"},
		},
	})
	return model
}

func TestMemoryCandidatesAcceptDropMarksJobDone(t *testing.T) {
	m := addTestProposal(t, testMemoryModel())
	if got := m.Pending(); got != 2 {
		t.Fatalf("pending = %d", got)
	}
	model, cmd := m.Update(pressKey("a"))
	m = model
	if cmd == nil || m.Pending() != 1 {
		t.Fatalf("after accept: cmd=%v pending=%d", cmd, m.Pending())
	}
	model, cmd = m.Update(pressKey("d"))
	m = model
	if cmd == nil || m.Pending() != 0 {
		t.Fatalf("after drop: cmd=%v pending=%d", cmd, m.Pending())
	}
}

func TestMemoryCandidatesEditChangesTypeAndTitle(t *testing.T) {
	m := addTestProposal(t, testMemoryModel())
	model, _ := m.Update(pressKey("e"))
	m = model
	if m.editing == nil {
		t.Fatal("edit did not open")
	}
	m.editing.title.SetValue("Edited")
	model, _ = m.Update(pressKey("tab"))
	m = model
	model, cmd := m.Update(pressKey("ctrl+s"))
	m = model
	if cmd == nil || m.editing != nil {
		t.Fatalf("save: cmd=%v editing=%v", cmd, m.editing)
	}
	got := m.proposals[0].candidates[0]
	if got.Title != "Edited" || got.Type != domain.ContextTypePitfall {
		t.Fatalf("candidate = %+v", got)
	}
}

func TestZeroCandidatesMarksDoneWithoutView(t *testing.T) {
	model, cmd := testMemoryModel().Update(memoryProposalMsg{host: "h1", job: memoryjob.Job{ID: "j1"}})
	m := model
	if cmd == nil || m.Pending() != 0 || len(m.proposals) != 0 {
		t.Fatalf("cmd=%v pending=%d proposals=%d", cmd, m.Pending(), len(m.proposals))
	}
}

func TestUnavailableSummariserLeavesJobQueued(t *testing.T) {
	model, cmd := testMemoryModel().Update(memoryProposalMsg{host: "h1", job: memoryjob.Job{ID: "j1"}, err: domain.ErrIntelligenceUnavailable})
	m := model
	if cmd != nil || m.Pending() != 0 || len(m.proposals) != 0 {
		t.Fatalf("cmd=%v pending=%d proposals=%d", cmd, m.Pending(), len(m.proposals))
	}
}

func TestMemoryPutParams(t *testing.T) {
	j := memoryjob.Job{Group: "G1", Repo: "owner/repo"}
	c := domain.MemoryCandidate{Type: domain.ContextTypePitfall, Title: `It's "x"`, Abstract: "a", Body: "b"}
	got := memoryPutParamsFor(j, c)
	if got.Group != "G1" || got.Repo != "" || got.Title != c.Title || got.Body != "b" || got.Type != "pitfall" {
		t.Fatalf("params = %+v", got)
	}
	j.Group = ""
	if got := memoryPutParamsFor(j, c); got.Repo != "owner/repo" {
		t.Fatalf("repo fallback = %+v", got)
	}
}

func TestMenuShowsProposalCount(t *testing.T) {
	if got := memoryMenuDesc(0); got != "No proposals" {
		t.Fatalf("zero = %q", got)
	}
	if got := memoryMenuDesc(3); got != "3 proposed notes to review" {
		t.Fatalf("three = %q", got)
	}
}

type fakeMemoryIntel struct {
	domain.IntelligenceProvider
	calls int
}

func (f *fakeMemoryIntel) ProposeMemories(context.Context, string) ([]domain.MemoryCandidate, error) {
	f.calls++
	return []domain.MemoryCandidate{{Type: domain.ContextTypeFact, Title: "From intel"}}, nil
}

func memoryCfg(aiOn bool) *config.Config {
	return &config.Config{AI: config.AIConfig{Enabled: aiOn}}
}

func TestMemoryJobWithCandidatesShowsWithoutIntel(t *testing.T) {
	intel := &fakeMemoryIntel{}
	m := NewMemoryCandidatesModel(memoryCfg(true), intel)
	job := memoryjob.Job{ID: "j1", Label: "PROJ-1", Candidates: []domain.MemoryCandidate{
		{Type: domain.ContextTypeDecision, Title: "Child", Abstract: "a", Body: "b"},
	}}
	m, cmd := m.Update(memoryJobsMsg{host: "h1", jobs: []memoryjob.Job{job}})
	if cmd != nil {
		_ = cmd()
	}
	if got := m.Pending(); got != 1 {
		t.Fatalf("pending = %d, want 1", got)
	}
	if intel.calls != 0 {
		t.Fatalf("intel calls = %d, want 0", intel.calls)
	}
	if m.busy {
		t.Fatal("busy after a job that needed no proposing")
	}
}

func TestMemoryJobWithoutCandidatesAIOffMarksDone(t *testing.T) {
	intel := &fakeMemoryIntel{}
	m := NewMemoryCandidatesModel(memoryCfg(false), intel)
	m, cmd := m.Update(memoryJobsMsg{host: "h1", jobs: []memoryjob.Job{{ID: "j1"}}})
	if cmd == nil {
		t.Fatal("no command for a job without candidates")
	}
	done, ok := cmd().(memoryJobDoneMsg)
	if !ok || done.job != "j1" {
		t.Fatalf("command returned %#v, want memoryJobDoneMsg for j1", done)
	}
	if intel.calls != 0 {
		t.Fatalf("intel calls = %d, want 0", intel.calls)
	}
	if m.busy {
		t.Fatal("busy with AI off")
	}
}

func TestMemoryJobWithoutCandidatesAIOnCallsIntel(t *testing.T) {
	intel := &fakeMemoryIntel{}
	m := NewMemoryCandidatesModel(memoryCfg(true), intel)
	_, cmd := m.Update(memoryJobsMsg{host: "h1", jobs: []memoryjob.Job{{ID: "j1", Source: "handover"}}})
	if cmd == nil {
		t.Fatal("no command for a job without candidates")
	}
	if _, ok := cmd().(memoryProposalMsg); !ok {
		t.Fatal("command did not propose memories")
	}
	if intel.calls != 1 {
		t.Fatalf("intel calls = %d, want 1", intel.calls)
	}
}
