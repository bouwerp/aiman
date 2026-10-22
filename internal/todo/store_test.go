package todo

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func add(text string) func(*domain.TodoList) error {
	return func(l *domain.TodoList) error {
		l.Items = append(l.Items, domain.TodoItem{Text: text, State: domain.TodoPending})
		return nil
	}
}

func TestMutateAssignsNeverReusedIDs(t *testing.T) {
	s := NewFiles(t.TempDir())
	_, _, _ = s.Mutate("s1", domain.AuthorUser, add("a"))
	_, _, _ = s.Mutate("s1", domain.AuthorUser, add("b"))
	_, _, _ = s.Mutate("s1", domain.AuthorUser, func(l *domain.TodoList) error {
		l.Items = l.Items[:1]
		return nil
	})
	l, _, err := s.Mutate("s1", domain.AuthorUser, add("c"))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Items) != 2 || l.Items[0].ID != "t1" || l.Items[1].ID != "t3" {
		t.Fatalf("items = %+v", l.Items)
	}
}

func TestMutateKeepsOneActive(t *testing.T) {
	s := NewFiles(t.TempDir())
	_, _, _ = s.Mutate("s1", domain.AuthorUser, add("a"))
	_, _, _ = s.Mutate("s1", domain.AuthorUser, add("b"))
	_, _, _ = s.Mutate("s1", domain.AuthorUser, func(l *domain.TodoList) error {
		l.Items[0].State = domain.TodoActive
		return nil
	})
	l, _, err := s.Mutate("s1", domain.AuthorUser, func(l *domain.TodoList) error {
		l.Items[1].State = domain.TodoActive
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if l.Items[0].State != domain.TodoPending || l.Items[1].State != domain.TodoActive {
		t.Fatalf("items = %+v", l.Items)
	}
}

func TestMutateNoopDoesNotBumpRev(t *testing.T) {
	root := t.TempDir()
	s := NewFiles(root)
	l, _, _ := s.Mutate("s1", domain.AuthorUser, add("a"))
	path := filepath.Join(root, "s1.json")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	l2, ch, err := s.Mutate("s1", domain.AuthorUser, func(*domain.TodoList) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if l2.Rev != l.Rev || !ch.Empty() || st.ModTime().After(old.Add(time.Minute)) {
		t.Fatalf("rev %d->%d change %+v mtime %v", l.Rev, l2.Rev, ch, st.ModTime())
	}
}

func TestMutateRejectsInvalid(t *testing.T) {
	s := NewFiles(t.TempDir())
	_, _, err := s.Mutate("s1", domain.AuthorUser, add("  "))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	_, _, err = s.Mutate("s1", domain.AuthorUser, func(l *domain.TodoList) error {
		l.Items = append(l.Items, domain.TodoItem{Text: "x", State: "bogus"})
		return nil
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentStateChangeKeepsUserAuthor(t *testing.T) {
	s := NewFiles(t.TempDir())
	l, _, _ := s.Mutate("s1", domain.AuthorUser, add("a"))
	at := l.Items[0].UpdatedAt
	s.now = func() time.Time { return at.Add(time.Minute) }
	l, ch, err := s.Mutate("s1", domain.AuthorAgent, func(l *domain.TodoList) error {
		l.Items[0].State = domain.TodoDone
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	it := l.Items[0]
	if it.State != domain.TodoDone || it.UpdatedBy != domain.AuthorUser || !it.UpdatedAt.Equal(at) {
		t.Fatalf("item = %+v", it)
	}
	if ch.SteersAgent() {
		t.Fatal("agent change must not steer")
	}
}

func TestMutateIsAtomicAndConcurrent(t *testing.T) {
	s := NewFiles(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.Mutate("s1", domain.AuthorUser, add("x")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	l, err := s.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, it := range l.Items {
		ids[it.ID] = true
	}
	if len(l.Items) != 20 || len(ids) != 20 || l.Rev != 20 {
		t.Fatalf("items=%d ids=%d rev=%d", len(l.Items), len(ids), l.Rev)
	}
}

func TestGetMissingListIsEmpty(t *testing.T) {
	l, err := NewFiles(t.TempDir()).Get("nope")
	if err != nil || l.SessionID != "nope" || l.Rev != 0 || len(l.Items) != 0 {
		t.Fatalf("l=%+v err=%v", l, err)
	}
}

func TestMutateRecordsTombstones(t *testing.T) {
	root := t.TempDir()
	s := NewFiles(root)
	_, _, _ = s.Mutate("s1", domain.AuthorUser, add("a"))
	_, _, _ = s.Mutate("s1", domain.AuthorUser, func(l *domain.TodoList) error { l.Items = nil; return nil })
	d, err := s.load("s1", filepath.Join(root, "s1.json"))
	if err != nil || len(d.Tombstones) != 1 || d.Tombstones[0].ID != "t1" || d.Tombstones[0].Text != "a" {
		t.Fatalf("d=%+v err=%v", d, err)
	}
}

func TestDiffRefOnly(t *testing.T) {
	s := NewFiles(t.TempDir())
	_, _, _ = s.Mutate("s1", domain.AuthorUser, add("a"))
	l, _, _ := s.Mutate("s1", domain.AuthorUser, add("b"))
	rev := l.Rev
	l, ch, err := s.Mutate("s1", domain.AuthorAiman, func(l *domain.TodoList) error {
		l.Items[1].Ref = "child:xyz"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Ops) != 1 || ch.Ops[0].Kind != OpRef || l.Rev != rev+1 || ch.SteersAgent() {
		t.Fatalf("ch=%+v rev=%d", ch, l.Rev)
	}
}

func TestIndexShiftKeepsStamp(t *testing.T) {
	setup := func() (*Files, domain.TodoItem) {
		s := NewFiles(t.TempDir())
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		s.now = func() time.Time { return base }
		_, _, _ = s.Mutate("s1", domain.AuthorAgent, add("a"))
		_, _, _ = s.Mutate("s1", domain.AuthorAgent, add("b"))
		l, _, _ := s.Mutate("s1", domain.AuthorUser, add("c"))
		s.now = func() time.Time { return base.Add(time.Hour) }
		return s, l.Items[1]
	}
	s, t2 := setup()
	l, _, err := s.Mutate("s1", domain.AuthorUser, func(l *domain.TodoList) error {
		l.Items = l.Items[1:]
		return nil
	})
	if err != nil || l.Items[0].ID != "t2" || l.Items[0].UpdatedBy != t2.UpdatedBy || !l.Items[0].UpdatedAt.Equal(t2.UpdatedAt) {
		t.Fatalf("remove: %+v want %+v err %v", l.Items[0], t2, err)
	}
	s, t2 = setup()
	l, _, err = s.Mutate("s1", domain.AuthorUser, func(l *domain.TodoList) error {
		l.Items = append([]domain.TodoItem{l.Items[0], {Text: "new", State: domain.TodoPending}}, l.Items[1:]...)
		return nil
	})
	if err != nil || l.Items[2].ID != "t2" || l.Items[2].UpdatedBy != t2.UpdatedBy || !l.Items[2].UpdatedAt.Equal(t2.UpdatedAt) {
		t.Fatalf("insert: %+v want %+v err %v", l.Items[2], t2, err)
	}
}
