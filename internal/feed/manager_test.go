package feed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/agentjob"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

var t0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

func TestMemoryCaps(t *testing.T) {
	m := NewMemory(filepath.Join(t.TempDir(), "memory.md"))
	var ops []MemoryOp
	for i := range 12 {
		ops = append(ops, MemoryOp{Op: "add", Text: fmt.Sprintf("note %d\nsecond line %s", i, strings.Repeat("x", 400))})
	}
	n, err := m.Apply(ops, t0)
	if err != nil || n != MaxMemoryOps {
		t.Fatalf("applied %d, %v; want %d", n, err, MaxMemoryOps)
	}
	bs, _ := m.Bullets()
	if len(bs) != MaxMemoryOps || bs[0].ID != "m1" || len(bs[0].Text) > MaxMemoryBullet || strings.Contains(bs[0].Text, "\n") {
		t.Fatalf("bullets = %d, first %+v", len(bs), bs[0])
	}
	if ok, _ := m.Forget("m1"); !ok {
		t.Fatal("forget m1")
	}
	if _, err := m.Apply([]MemoryOp{{Op: "add", Text: "after"}, {Op: "remove", ID: "m2"}}, t0); err != nil {
		t.Fatal(err)
	}
	bs, _ = m.Bullets()
	if last := bs[len(bs)-1]; last.ID != "m11" || last.Text != "after" {
		t.Errorf("new bullet = %+v; ids are never reused", last)
	}
	for range 8 {
		big := make([]MemoryOp, MaxMemoryOps)
		for i := range big {
			big[i] = MemoryOp{Op: "add", Text: strings.Repeat("y", MaxMemoryBullet)}
		}
		if _, err := m.Apply(big, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	fi, _ := os.Stat(m.Path)
	bs, _ = m.Bullets()
	if fi.Size() > MaxMemoryBytes || bs[0].ID == "m3" {
		t.Errorf("file %d bytes, first %s; want at most 16 KiB with the oldest dropped", fi.Size(), bs[0].ID)
	}
}

type stubEvents struct{ events []domain.FeedEvent }

func (s *stubEvents) After(_ context.Context, after int64, limit int) ([]domain.FeedEvent, error) {
	var out []domain.FeedEvent
	for _, e := range s.events {
		if e.ID > after && (limit <= 0 || len(out) < limit) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *stubEvents) ByIDs(_ context.Context, ids []int64) ([]domain.FeedEvent, error) {
	var out []domain.FeedEvent
	for _, e := range s.events {
		for _, id := range ids {
			if id == e.ID {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

func (s *stubEvents) Thread(context.Context, string, int64, int) ([]domain.FeedEvent, error) {
	return nil, nil
}

func (s *stubEvents) Mutes(context.Context, time.Time) ([]domain.FeedMute, error) { return nil, nil }

func repoFor(t *testing.T) *sqlite.Repository {
	t.Helper()
	repo, err := sqlite.NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func slackEv(id int64, class, channel string) domain.FeedEvent {
	return domain.FeedEvent{ID: id, Source: "slack", ExternalID: fmt.Sprintf("%s/%d", channel, id), At: t0, Actor: "U1", Kind: "message",
		Body: fmt.Sprintf("body-%d", id), ThreadKey: fmt.Sprintf("slack:%s/%d", channel, id), Class: domain.FeedEventClass(class), Channel: channel}
}

// A digest is stored with a negative id, the agent may answer it, and the
// options are checked: a draft goes only to a channel an answered event came
// from, a to-do only to a visible session.
func TestManagerDigestsAndOptionChecks(t *testing.T) {
	repo := repoFor(t)
	ev := &stubEvents{events: []domain.FeedEvent{slackEv(1, "direct", "D1"), slackEv(2, "ambient", "C7"), slackEv(3, "ambient", "C7")}}
	var notified []domain.FeedAction
	var stdin string
	m, err := NewManager(ManagerOptions{
		Events: DigestReader{EventReader: ev, Digests: repo}, Store: repo, Digests: repo,
		Now: func() time.Time { return t0 }, Dir: t.TempDir(), Agent: "claude", Interval: time.Minute,
		Context: func(context.Context) ([]SessionFacts, error) { return []SessionFacts{{ID: "s1", Name: "web"}}, nil },
		RunJob: func(_ context.Context, j agentjob.Job) (agentjob.JobResult, error) {
			b, _ := os.ReadFile(filepath.Join(j.Dir, "batch.md"))
			stdin = string(b)
			acts := []string{
				`{"kind":"slack_draft","priority":"high","rationale":"dm","answers":[1],"options":[{"label":"ok","params":{"channel":"D1","text":"hi"}},{"label":"elsewhere","params":{"channel":"C9","text":"x"}}]}`,
				`{"kind":"todo_add","priority":"low","rationale":"digest","answers":[-1],"options":[{"label":"t","params":{"session":"web","text":"look"}}]}`,
				`{"kind":"todo_add","priority":"low","rationale":"ghost","answers":[2],"options":[{"label":"t","params":{"session":"nope","text":"x"}}]}`,
			}
			out := `{"actions":[` + strings.Join(acts, ",") + `],"digest":[{"event":3,"line":"chatter"}],"memory_ops":[{"op":"add","text":"likes drafts"}],"summary":"s"}`
			return agentjob.JobResult{Stdout: out}, nil
		},
		OnAction: func(a domain.FeedAction) { notified = append(notified, a) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdin, "Event -1") || !strings.Contains(stdin, "[2] U1: body-2") {
		t.Errorf("batch.md lacks the stored digest:\n%s", stdin)
	}
	acts, _ := repo.ListFeedActions(context.Background(), nil, 100)
	if len(acts) != 2 || len(notified) != 2 {
		t.Fatalf("stored %d, notified %d; want 2", len(acts), len(notified))
	}
	for _, a := range acts {
		switch a.Kind {
		case domain.FeedSlackDraft:
			if len(a.Options) != 1 || a.Options[0].Label != "ok" {
				t.Errorf("draft options = %+v; the C9 option is dropped", a.Options)
			}
		case domain.FeedTodoAdd:
			if a.Answers[0] != -1 {
				t.Errorf("todo answers = %v", a.Answers)
			}
		}
	}
	digests, _ := repo.FeedDigests(context.Background(), []int64{-1})
	if len(digests) != 1 || digests[0].Channel != "C7" {
		t.Errorf("stored digests = %+v", digests)
	}
	if lines := ReadDigestLines(m.o.Dir, 1); lines[3] != "chatter" {
		t.Errorf("digest lines = %v", lines)
	}
	if bs, _ := m.Memory().Bullets(); len(bs) != 1 {
		t.Errorf("memory = %+v", bs)
	}
	if _, err := os.Stat(filepath.Join(m.o.Dir, "batches", "1")); !os.IsNotExist(err) {
		t.Errorf("batch directory kept after the run: %v", err)
	}
}

// An unreadable reply gets one more run naming the error.
func TestManagerRetriesUnreadableReplyOnce(t *testing.T) {
	repo := repoFor(t)
	ev := &stubEvents{events: []domain.FeedEvent{slackEv(1, "direct", "D1")}}
	var briefs []string
	m, _ := NewManager(ManagerOptions{
		Events: ev, Store: repo, Now: func() time.Time { return t0 }, Dir: t.TempDir(), Agent: "claude",
		RunJob: func(_ context.Context, j agentjob.Job) (agentjob.JobResult, error) {
			briefs = append(briefs, j.Brief)
			if len(briefs) == 1 {
				return agentjob.JobResult{Stdout: "no json here"}, nil
			}
			return agentjob.JobResult{Stdout: `{"summary":"fine"}`}, nil
		},
	})
	if err := m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _ := repo.GetFeedBatch(context.Background(), 1)
	if len(briefs) != 2 || !strings.Contains(briefs[1], "could not be read") || b.State != BatchDone || b.Summary != "fine" {
		t.Errorf("briefs %q, batch %+v", briefs, b)
	}
}

// Events of a switched-off source never join a batch.
func TestPendingSkipsOffSources(t *testing.T) {
	aimanEv := slackEv(2, "direct", "D2")
	aimanEv.Source = "aiman"
	ev := &stubEvents{events: []domain.FeedEvent{slackEv(1, "direct", "D1"), aimanEv, slackEv(3, "direct", "D3")}}
	m, err := NewManager(ManagerOptions{
		Events: ev, Store: repoFor(t), Now: func() time.Time { return t0 }, Dir: t.TempDir(), Agent: "claude",
		Interval: time.Minute, OffSources: []domain.FeedSource{"aiman"},
		RunJob: func(context.Context, agentjob.Job) (agentjob.JobResult, error) { return agentjob.JobResult{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Fatalf("pending = %+v; want events 1 and 3", got)
	}
}
