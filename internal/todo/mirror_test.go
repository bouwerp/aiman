package todo

import (
	"reflect"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

var (
	delivered = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	before    = delivered.Add(-time.Minute)
	after     = delivered.Add(time.Minute)
	mergeNow  = delivered.Add(time.Hour)
)

func item(id, text string, st domain.TodoState, by domain.TodoAuthor, at time.Time) domain.TodoItem {
	return domain.TodoItem{ID: id, Text: text, State: st, UpdatedBy: by, UpdatedAt: at}
}

func snapItem(text string, st domain.TodoState) domain.TodoSnapshotItem {
	return domain.TodoSnapshotItem{Text: text, Status: st}
}

func ids(l domain.TodoList) []string {
	out := make([]string, len(l.Items))
	for i, it := range l.Items {
		out[i] = it.ID
	}
	return out
}

func byID(t *testing.T, l domain.TodoList, id string) domain.TodoItem {
	t.Helper()
	for _, it := range l.Items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("no item %q in %+v", id, l.Items)
	return domain.TodoItem{}
}

func TestNormalizeAndSimilarity(t *testing.T) {
	if got := Normalize("  Run   preprod --check! "); got != "run preprod check" {
		t.Fatalf("Normalize = %q", got)
	}
	if s := Similarity("Add rollback test to deploy script", "Add a rollback test to deploy script"); s < 0.8 {
		t.Fatalf("similarity = %v", s)
	}
	if s := Similarity("Write docs", "Fix the build"); s >= 0.8 {
		t.Fatalf("similarity = %v", s)
	}
	if s := Similarity("", ""); s != 0 {
		t.Fatalf("empty similarity = %v", s)
	}
}

func TestMergeExactAndFuzzyMatch(t *testing.T) {
	cases := []struct {
		name, existing, snap string
		match                bool
	}{
		{"exact after normalize", "Run preprod --check", "run preprod check", true},
		{"fuzzy at threshold", "Add rollback test to deploy script", "Add a rollback test to deploy script", true},
		{"below threshold", "Add rollback test", "Add a rollback test to deploy script", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list := domain.TodoList{Items: []domain.TodoItem{item("t1", tc.existing, domain.TodoPending, domain.AuthorAgent, before)}}
			got := Merge(list, nil, []domain.TodoSnapshotItem{snapItem(tc.snap, domain.TodoActive)}, delivered, mergeNow)
			if tc.match {
				if len(got.Items) != 1 || got.Items[0].ID != "t1" || got.Items[0].State != domain.TodoActive {
					t.Fatalf("items = %+v", got.Items)
				}
				return
			}
			if len(got.Items) != 2 || got.Items[0].State != domain.TodoDropped || got.Items[1].ID != "" {
				t.Fatalf("items = %+v", got.Items)
			}
		})
	}
}

func TestMergeAgentItemTakesSnapshot(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		item("t1", "Write the migration", domain.TodoPending, domain.AuthorAgent, before),
		item("t2", "Run the tests", domain.TodoPending, domain.AuthorAgent, before),
	}}
	snap := []domain.TodoSnapshotItem{
		snapItem("Run the tests", domain.TodoActive),
		snapItem("Write the migration.", domain.TodoDone),
	}
	got := Merge(list, nil, snap, delivered, mergeNow)
	if !reflect.DeepEqual(ids(got), []string{"t2", "t1"}) {
		t.Fatalf("order = %v", ids(got))
	}
	t1 := byID(t, got, "t1")
	if t1.Text != "Write the migration." || t1.State != domain.TodoDone {
		t.Fatalf("t1 = %+v", t1)
	}
	if byID(t, got, "t2").State != domain.TodoActive {
		t.Fatalf("t2 = %+v", byID(t, got, "t2"))
	}
}

func TestMergeUserItemTakesOnlyStatus(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		item("t1", "Run preprod --check", domain.TodoPending, domain.AuthorUser, before),
	}}
	got := Merge(list, nil, []domain.TodoSnapshotItem{snapItem("run preprod check", domain.TodoActive)}, delivered, mergeNow)
	t1 := byID(t, got, "t1")
	if t1.Text != "Run preprod --check" || t1.UpdatedBy != domain.AuthorUser || t1.State != domain.TodoActive {
		t.Fatalf("t1 = %+v", t1)
	}
}

func TestMergeKeepsUndeliveredUserState(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		item("t1", "Write code", domain.TodoDone, domain.AuthorAgent, before),
		item("t2", "Deploy", domain.TodoDone, domain.AuthorUser, after),
	}}
	snap := []domain.TodoSnapshotItem{snapItem("Write code", domain.TodoDone), snapItem("Deploy", domain.TodoActive)}
	got := Merge(list, nil, snap, delivered, mergeNow)
	if st := byID(t, got, "t2").State; st != domain.TodoDone {
		t.Fatalf("t2 state = %q", st)
	}
}

func TestMergeDropsUnmatchedAgentItems(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		item("t1", "Write code", domain.TodoPending, domain.AuthorAgent, before),
		item("t2", "Old plan step", domain.TodoPending, domain.AuthorAgent, before),
	}}
	got := Merge(list, nil, []domain.TodoSnapshotItem{snapItem("Write code", domain.TodoActive)}, delivered, mergeNow)
	if len(got.Items) != 2 || byID(t, got, "t2").State != domain.TodoDropped {
		t.Fatalf("items = %+v", got.Items)
	}
	if len(got.NotInAgent) != 0 {
		t.Fatalf("NotInAgent = %v", got.NotInAgent)
	}
}

func TestMergeEmptySnapshotDropsAgentItemsVisibly(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		item("t1", "Write code", domain.TodoActive, domain.AuthorAgent, before),
	}}
	got := Merge(list, nil, []domain.TodoSnapshotItem{{Text: "  ?! ", Status: "bogus"}}, delivered, mergeNow)
	if len(got.Items) != 1 || got.Items[0].ID != "t1" || got.Items[0].State != domain.TodoDropped {
		t.Fatalf("items = %+v", got.Items)
	}
}

func TestMergeMarksDeliveredOmission(t *testing.T) {
	base := []domain.TodoItem{
		item("t1", "Write code", domain.TodoActive, domain.AuthorAgent, before),
		item("t4", "Add rollback test", domain.TodoPending, domain.AuthorUser, before),
	}
	snap := []domain.TodoSnapshotItem{snapItem("Write code", domain.TodoActive)}

	got := Merge(domain.TodoList{Items: base}, nil, snap, delivered, mergeNow)
	if !reflect.DeepEqual(got.NotInAgent, []string{"t4"}) {
		t.Fatalf("delivered NotInAgent = %v", got.NotInAgent)
	}
	if byID(t, got, "t4") != base[1] {
		t.Fatalf("t4 changed: %+v", byID(t, got, "t4"))
	}

	undelivered := append([]domain.TodoItem(nil), base...)
	undelivered[1].UpdatedAt = after
	got = Merge(domain.TodoList{Items: undelivered, NotInAgent: []string{"t4"}}, nil, snap, delivered, mergeNow)
	if len(got.NotInAgent) != 0 {
		t.Fatalf("undelivered NotInAgent = %v", got.NotInAgent)
	}
}

func TestMergeSkipsUndeliveredTombstone(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{item("t1", "Write code", domain.TodoActive, domain.AuthorAgent, before)}}
	snap := []domain.TodoSnapshotItem{snapItem("Write code", domain.TodoActive), snapItem("Write docs", domain.TodoPending)}

	tombs := []Tombstone{{ID: "t2", Text: "write docs.", RemovedAt: after}}
	got := Merge(list, tombs, snap, delivered, mergeNow)
	if len(got.Items) != 1 {
		t.Fatalf("re-added removed item: %+v", got.Items)
	}

	tombs[0].RemovedAt = before
	got = Merge(list, tombs, snap, delivered, mergeNow)
	if len(got.Items) != 2 || got.Items[1].Text != "Write docs" || got.Items[1].ID != "" {
		t.Fatalf("delivered removal should allow re-add: %+v", got.Items)
	}
}

func TestMergeKeepsOrderWhileUndelivered(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		item("t3", "Three", domain.TodoPending, domain.AuthorUser, after),
		item("t1", "One", domain.TodoActive, domain.AuthorAgent, before),
		item("t2", "Two", domain.TodoPending, domain.AuthorAgent, before),
	}}
	snap := []domain.TodoSnapshotItem{
		snapItem("One", domain.TodoActive), snapItem("Two", domain.TodoPending),
		snapItem("Three", domain.TodoPending), snapItem("Four", domain.TodoPending),
	}
	got := Merge(list, nil, snap, delivered, mergeNow)
	if !reflect.DeepEqual(ids(got), []string{"t3", "t1", "t2", ""}) {
		t.Fatalf("order = %v", ids(got))
	}

	list.Items[0].UpdatedAt = before
	got = Merge(list, nil, snap, delivered, mergeNow)
	if !reflect.DeepEqual(ids(got), []string{"t1", "t2", "t3", ""}) {
		t.Fatalf("delivered order = %v", ids(got))
	}
}

func TestMergeKeepsUnmatchedItemsInPlace(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		item("t1", "One", domain.TodoPending, domain.AuthorAgent, before),
		item("t5", "User step", domain.TodoPending, domain.AuthorUser, before),
		item("t2", "Two", domain.TodoPending, domain.AuthorAgent, before),
		item("t6", "Gone step", domain.TodoPending, domain.AuthorAgent, before),
		item("t3", "Three", domain.TodoPending, domain.AuthorAgent, before),
	}}
	snap := []domain.TodoSnapshotItem{
		snapItem("New first", domain.TodoPending),
		snapItem("Three", domain.TodoPending), snapItem("One", domain.TodoPending), snapItem("Two", domain.TodoPending),
	}
	got := Merge(list, nil, snap, delivered, mergeNow)
	if !reflect.DeepEqual(ids(got), []string{"", "t3", "t1", "t5", "t2", "t6"}) {
		t.Fatalf("order = %v", ids(got))
	}
}

func TestMergeEachExistingMatchesOnce(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{item("t1", "Run tests", domain.TodoPending, domain.AuthorAgent, before)}}
	snap := []domain.TodoSnapshotItem{snapItem("Run tests", domain.TodoDone), snapItem("Run tests", domain.TodoPending)}
	got := Merge(list, nil, snap, delivered, mergeNow)
	if len(got.Items) != 2 || got.Items[0].ID != "t1" || got.Items[0].State != domain.TodoDone || got.Items[1].ID != "" {
		t.Fatalf("items = %+v", got.Items)
	}
}

func TestMergeFuzzyPrefersBestScore(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		item("t1", "add a rollback test to the deploy script", domain.TodoPending, domain.AuthorAgent, before),
		item("t2", "add rollback test to the deploy script", domain.TodoPending, domain.AuthorAgent, before),
	}}
	snap := []domain.TodoSnapshotItem{
		snapItem("add the rollback test to the deploy script", domain.TodoPending),
		snapItem("add a rollback test to the deploy script now", domain.TodoActive),
	}
	got := Merge(list, nil, snap, delivered, mergeNow)
	if byID(t, got, "t1").State != domain.TodoActive || len(got.Items) != 2 {
		t.Fatalf("items = %+v", got.Items)
	}
}

func TestMirrorNeverProducesAgentNoteAuthor(t *testing.T) {
	s := NewFiles(t.TempDir())
	if _, _, err := s.Mutate("s1", domain.AuthorUser, add("Write docs")); err != nil {
		t.Fatal(err)
	}
	l, ch, err := s.Mirror("s1", []domain.TodoSnapshotItem{
		snapItem("Write docs", domain.TodoActive), snapItem("Run tests", domain.TodoPending),
	}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if ch.Author != domain.AuthorAgent || ch.SteersAgent() {
		t.Fatalf("change = %+v", ch)
	}
	if !reflect.DeepEqual(ids(l), []string{"t1", "t2"}) || l.Items[1].UpdatedBy != domain.AuthorAgent {
		t.Fatalf("items = %+v", l.Items)
	}
	if l.Items[0].UpdatedBy != domain.AuthorUser || l.Items[0].State != domain.TodoActive {
		t.Fatalf("t1 = %+v", l.Items[0])
	}
}

func TestMirrorPersistsNotInAgentOnlyChange(t *testing.T) {
	s := NewFiles(t.TempDir())
	if _, _, err := s.Mutate("s1", domain.AuthorUser, add("Write docs")); err != nil {
		t.Fatal(err)
	}
	prev, _ := s.Get("s1")
	l, ch, err := s.Mirror("s1", nil, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(l.NotInAgent, []string{"t1"}) || l.Rev != prev.Rev+1 || !ch.Empty() || ch.Rev != l.Rev {
		t.Fatalf("list = %+v change = %+v", l, ch)
	}
	got, _ := s.Get("s1")
	if !reflect.DeepEqual(got.NotInAgent, []string{"t1"}) || got.Rev != l.Rev {
		t.Fatalf("stored = %+v", got)
	}
	again, _, _ := s.Mirror("s1", nil, time.Now().Add(time.Hour))
	if again.Rev != l.Rev {
		t.Fatalf("no-op mirror bumped rev: %d", again.Rev)
	}
}

func TestMirrorKeepsOneActive(t *testing.T) {
	s := NewFiles(t.TempDir())
	l, _, err := s.Mirror("s1", []domain.TodoSnapshotItem{
		snapItem("A", domain.TodoActive), snapItem("B", domain.TodoActive),
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if l.Items[0].State != domain.TodoPending || l.Items[1].State != domain.TodoActive {
		t.Fatalf("items = %+v", l.Items)
	}
}

// clockFiles returns a store whose clock reads *at.
func clockFiles(t *testing.T, at *time.Time) *Files {
	t.Helper()
	s := NewFiles(t.TempDir())
	s.now = func() time.Time { return *at }
	return s
}

func TestMirrorKeepsUndeliveredUserActive(t *testing.T) {
	clock := before
	s := clockFiles(t, &clock)
	if _, _, err := s.Mirror("s1", []domain.TodoSnapshotItem{snapItem("Write code", domain.TodoPending)}, before); err != nil {
		t.Fatal(err)
	}
	clock = after
	if _, _, err := s.Mutate("s1", domain.AuthorUser, func(l *domain.TodoList) error {
		l.Items = append(l.Items, domain.TodoItem{Text: "Deploy", State: domain.TodoActive})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	clock = mergeNow
	l, _, err := s.Mirror("s1", []domain.TodoSnapshotItem{
		snapItem("Write code", domain.TodoActive), snapItem("Next step", domain.TodoActive),
	}, delivered)
	if err != nil {
		t.Fatal(err)
	}
	if st := byID(t, l, "t2").State; st != domain.TodoActive {
		t.Fatalf("user's active item demoted: %+v", l.Items)
	}
	for _, it := range l.Items {
		if it.ID != "t2" && it.State == domain.TodoActive {
			t.Fatalf("second active item: %+v", l.Items)
		}
	}
}

func TestMirrorMatchesUndeliveredRetext(t *testing.T) {
	clock := before
	s := clockFiles(t, &clock)
	if _, _, err := s.Mutate("s1", domain.AuthorUser, add("Run preprod --check")); err != nil {
		t.Fatal(err)
	}
	clock = after
	if _, _, err := s.Mutate("s1", domain.AuthorUser, func(l *domain.TodoList) error {
		l.Items[0].Text = "Run preprod --check --dry"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	clock = mergeNow
	l, _, err := s.Mirror("s1", []domain.TodoSnapshotItem{snapItem("Run preprod --check", domain.TodoActive)}, delivered)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Items) != 1 || l.Items[0].Text != "Run preprod --check --dry" || l.Items[0].State != domain.TodoPending {
		t.Fatalf("items = %+v", l.Items)
	}
	if len(l.NotInAgent) != 0 {
		t.Fatalf("NotInAgent = %v", l.NotInAgent)
	}
}

func TestMergeRetextRecordOnlyWhileUndelivered(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{item("t1", "Run preprod --check --dry", domain.TodoPending, domain.AuthorUser, before)}}
	rec := []Retext{{ID: "t1", OldText: Normalize("Run preprod --check"), At: before}}
	got := MergeWithRetexts(list, nil, rec, []domain.TodoSnapshotItem{snapItem("Run preprod --check", domain.TodoActive)}, delivered, mergeNow)
	if len(got.Items) != 2 {
		t.Fatalf("delivered retext should not match: %+v", got.Items)
	}
}
