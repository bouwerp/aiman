package notify

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

type fakeSource struct {
	entries []domain.Notification
	last    int64
	since   int
}

func (f *fakeSource) Since(ctx context.Context, after int64, limit int) ([]domain.Notification, int64, error) {
	f.since++
	if after == -1 && limit == 0 {
		if f.last == 0 && len(f.entries) > 0 {
			f.last = f.entries[len(f.entries)-1].Seq
		}
		return nil, f.last, nil
	}
	var out []domain.Notification
	for _, e := range f.entries {
		if e.Seq > after {
			out = append(out, e)
			if len(out) >= limit && limit > 0 {
				break
			}
		}
	}
	last := f.last
	if len(f.entries) > 0 {
		last = f.entries[len(f.entries)-1].Seq
	}
	return out, last, nil
}

type memCursor struct {
	seq int64
	ok  bool
}

func (m *memCursor) Load(ctx context.Context) (int64, bool, error) {
	return m.seq, m.ok, nil
}

func (m *memCursor) Store(ctx context.Context, seq int64) error {
	m.seq = seq
	m.ok = true
	return nil
}

type fakeSink struct {
	name    string
	failSeq map[int64]int
	sent    []int64
}

func (f *fakeSink) Name() string { return f.name }
func (f *fakeSink) Send(ctx context.Context, n domain.Notification) error {
	if f.failSeq != nil {
		if c := f.failSeq[n.Seq]; c > 0 {
			f.failSeq[n.Seq] = c - 1
			return os.ErrInvalid
		}
	}
	f.sent = append(f.sent, n.Seq)
	return nil
}

func TestDispatcherStartsAtTailWithoutCursor(t *testing.T) {
	src := &fakeSource{entries: []domain.Notification{{Seq: 1}, {Seq: 2}, {Seq: 3}}}
	cur := &memCursor{}
	a := &fakeSink{name: "a"}
	d := NewDispatcher(src, cur, a)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(a.sent) != 0 {
		t.Fatalf("sent = %#v", a.sent)
	}
	if cur.seq != 3 || !cur.ok {
		t.Fatalf("cursor = %d %v", cur.seq, cur.ok)
	}
}

func TestDispatcherDeliversInOrder(t *testing.T) {
	src := &fakeSource{entries: []domain.Notification{{Seq: 1}, {Seq: 2}, {Seq: 3}}}
	cur := &memCursor{seq: 0, ok: true}
	a := &fakeSink{name: "a"}
	d := NewDispatcher(src, cur, a)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(a.sent) != 3 || a.sent[0] != 1 || a.sent[1] != 2 || a.sent[2] != 3 {
		t.Fatalf("sent = %#v", a.sent)
	}
	if cur.seq != 3 {
		t.Fatalf("cursor = %d", cur.seq)
	}
}

func TestDispatcherRetriesFailedSinkOnly(t *testing.T) {
	src := &fakeSource{entries: []domain.Notification{{Seq: 1}}}
	cur := &memCursor{seq: 0, ok: true}
	a := &fakeSink{name: "a"}
	b := &fakeSink{name: "b", failSeq: map[int64]int{1: 1}}
	d := NewDispatcher(src, cur, a, b)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(a.sent) != 1 || len(b.sent) != 0 || cur.seq != 0 {
		t.Fatalf("tick1 a=%#v b=%#v cur=%d", a.sent, b.sent, cur.seq)
	}
	if err := d.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(a.sent) != 1 || len(b.sent) != 1 || cur.seq != 1 {
		t.Fatalf("tick2 a=%#v b=%#v cur=%d", a.sent, b.sent, cur.seq)
	}
}

func TestDispatcherSkipsAfterMaxAttempts(t *testing.T) {
	src := &fakeSource{entries: []domain.Notification{{Seq: 1}, {Seq: 2}}}
	cur := &memCursor{seq: 0, ok: true}
	a := &fakeSink{name: "a"}
	b := &fakeSink{name: "b", failSeq: map[int64]int{1: 100}}
	d := NewDispatcher(src, cur, a, b)
	for i := 0; i < 4; i++ {
		if err := d.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if cur.seq != 2 {
		t.Fatalf("after skip cursor=%d", cur.seq)
	}
	if len(b.sent) != 1 || len(a.sent) != 2 {
		t.Fatalf("a=%#v b=%#v", a.sent, b.sent)
	}
}

func TestFileCursorRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cursor")
	c := &FileCursor{Path: p}
	seq, ok, err := c.Load(context.Background())
	if err != nil || ok || seq != 0 {
		t.Fatalf("missing: %d %v %v", seq, ok, err)
	}
	if err := c.Store(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	seq, ok, err = c.Load(context.Background())
	if err != nil || !ok || seq != 42 {
		t.Fatalf("load: %d %v %v", seq, ok, err)
	}
}
