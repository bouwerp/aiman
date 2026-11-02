package todo

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// seen marks an item as reported by the last applied snapshot.
func seen(it domain.TodoItem, text string, st domain.TodoState) domain.TodoItem {
	it.Seen = &domain.SnapshotView{Text: text, State: st}
	return it
}

// The headline bug: the agent ticks an item through the CLI while its own
// task list still shows it open; the next stale snapshot must not reopen it.
func TestMergeStaleSnapshotKeepsAgentCLIChange(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		seen(item("t1", "Write code", domain.TodoDone, domain.AuthorAgent, mergeNow), "Write code", domain.TodoActive),
	}}
	got := Merge(list, nil, []domain.TodoSnapshotItem{snapItem("Write code", domain.TodoActive)}, delivered, mergeNow)
	if st := byID(t, got, "t1").State; st != domain.TodoDone {
		t.Fatalf("stale snapshot reopened a CLI change: %+v", byID(t, got, "t1"))
	}
}

// The agent then ticks the same item in its own list: the snapshot differs
// from the merge base, so the tick lands.
func TestMergeAppliesAgentActAfterCLIChange(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		seen(item("t1", "Write code", domain.TodoPending, domain.AuthorAgent, mergeNow), "Write code", domain.TodoPending),
	}}
	got := Merge(list, nil, []domain.TodoSnapshotItem{snapItem("Write code", domain.TodoDone)}, delivered, mergeNow)
	t1 := byID(t, got, "t1")
	if t1.State != domain.TodoDone {
		t.Fatalf("agent's own tick did not land: %+v", t1)
	}
	if t1.Seen == nil || t1.Seen.State != domain.TodoDone {
		t.Fatalf("merge base not advanced: %+v", t1)
	}
}

// An item the agent added through the CLI is not in its own list; a snapshot
// keeps it and flags it instead of dropping it.
func TestMergeKeepsCLIAddedItemMissingFromSnapshot(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		seen(item("t1", "Write code", domain.TodoActive, domain.AuthorAgent, mergeNow), "Write code", domain.TodoActive),
		item("t2", "Write the failing test", domain.TodoPending, domain.AuthorAgent, mergeNow),
	}}
	got := Merge(list, nil, []domain.TodoSnapshotItem{snapItem("Write code", domain.TodoActive)}, delivered, mergeNow)
	if st := byID(t, got, "t2").State; st != domain.TodoPending {
		t.Fatalf("CLI-added item changed: %+v", byID(t, got, "t2"))
	}
	if !reflect.DeepEqual(got.NotInAgent, []string{"t2"}) {
		t.Fatalf("NotInAgent = %v", got.NotInAgent)
	}
}

// A mirrored item the agent removed from its own list, untouched since, is
// dropped.
func TestMergeDropsAgentRemovedUntouchedItem(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		seen(item("t1", "Write code", domain.TodoActive, domain.AuthorAgent, before), "Write code", domain.TodoActive),
		seen(item("t2", "Old plan step", domain.TodoPending, domain.AuthorAgent, before), "Old plan step", domain.TodoPending),
	}}
	got := Merge(list, nil, []domain.TodoSnapshotItem{snapItem("Write code", domain.TodoActive)}, delivered, mergeNow)
	if st := byID(t, got, "t2").State; st != domain.TodoDropped {
		t.Fatalf("t2 = %+v", byID(t, got, "t2"))
	}
	if len(got.NotInAgent) != 0 {
		t.Fatalf("NotInAgent = %v", got.NotInAgent)
	}
}

// The same removal, but the item was changed since the last snapshot: kept.
func TestMergeKeepsAgentRemovedTouchedItem(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		seen(item("t1", "Write code", domain.TodoActive, domain.AuthorAgent, before), "Write code", domain.TodoActive),
		seen(item("t2", "Old plan step", domain.TodoDone, domain.AuthorAgent, mergeNow), "Old plan step", domain.TodoPending),
	}}
	got := Merge(list, nil, []domain.TodoSnapshotItem{snapItem("Write code", domain.TodoActive)}, delivered, mergeNow)
	if st := byID(t, got, "t2").State; st != domain.TodoDone {
		t.Fatalf("touched item dropped or reverted: %+v", byID(t, got, "t2"))
	}
	if !reflect.DeepEqual(got.NotInAgent, []string{"t2"}) {
		t.Fatalf("NotInAgent = %v", got.NotInAgent)
	}
}

// An operator tick can never be reverted by a stale snapshot, before or after
// the steering note is delivered.
func TestMergeNeverRevertsOperatorTick(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		seen(item("t1", "Deploy", domain.TodoDone, domain.AuthorUser, before), "Deploy", domain.TodoActive),
	}}
	got := Merge(list, nil, []domain.TodoSnapshotItem{snapItem("Deploy", domain.TodoActive)}, delivered, mergeNow)
	if st := byID(t, got, "t1").State; st != domain.TodoDone {
		t.Fatalf("stale snapshot reverted the operator: %+v", byID(t, got, "t1"))
	}
}

// The agent ticking an operator's item in its own list is an act and lands.
func TestMergeAppliesAgentTickOfOperatorItem(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		seen(item("t1", "Deploy", domain.TodoPending, domain.AuthorUser, before), "Deploy", domain.TodoPending),
	}}
	got := Merge(list, nil, []domain.TodoSnapshotItem{snapItem("Deploy", domain.TodoDone)}, delivered, mergeNow)
	t1 := byID(t, got, "t1")
	if t1.State != domain.TodoDone || t1.UpdatedBy != domain.AuthorUser {
		t.Fatalf("t1 = %+v", t1)
	}
}

// A gate keeps its state whatever the snapshot says, open or closed.
func TestMergeNeverTouchesGate(t *testing.T) {
	for _, st := range []domain.TodoState{domain.TodoPending, domain.TodoDone} {
		list := domain.TodoList{Items: []domain.TodoItem{
			seen(item("t1", DesignGateText, st, domain.AuthorAiman, before), DesignGateText, domain.TodoPending),
		}}
		list.Items[0].Ref = DesignGateRef
		got := Merge(list, nil, []domain.TodoSnapshotItem{snapItem(DesignGateText, domain.TodoDone)}, delivered, mergeNow)
		if got := byID(t, got, "t1"); got.State != st {
			t.Fatalf("gate %q -> %q", st, got.State)
		}
	}
}

// The Done-when item is evidence-backed: a snapshot can neither tick it nor
// reopen it.
func TestMergeNeverTouchesDoneItem(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		seen(item("t1", DoneItemText, domain.TodoPending, domain.AuthorAiman, before), DoneItemText, domain.TodoPending),
	}}
	list.Items[0].Ref = DoneRef
	got := Merge(list, nil, []domain.TodoSnapshotItem{snapItem(DoneItemText, domain.TodoDone)}, delivered, mergeNow)
	if st := byID(t, got, "t1").State; st != domain.TodoPending {
		t.Fatalf("snapshot ticked the done item: %+v", byID(t, got, "t1"))
	}
}

// A kept active item blocks the snapshot from activating another item.
func TestMergeSnapshotCannotStealActive(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		seen(item("t1", "Write code", domain.TodoActive, domain.AuthorAgent, mergeNow), "Write code", domain.TodoPending),
		seen(item("t2", "Run tests", domain.TodoPending, domain.AuthorAgent, before), "Run tests", domain.TodoPending),
	}}
	// t1 is active from a CLI start; the stale snapshot still shows it pending
	// and marks t2 in_progress.
	snap := []domain.TodoSnapshotItem{
		snapItem("Write code", domain.TodoPending),
		snapItem("Run tests", domain.TodoActive),
	}
	got := Merge(list, nil, snap, delivered, mergeNow)
	if st := byID(t, got, "t1").State; st != domain.TodoActive {
		t.Fatalf("CLI active choice lost: %+v", byID(t, got, "t1"))
	}
	if st := byID(t, got, "t2").State; st != domain.TodoPending {
		t.Fatalf("snapshot stole active: %+v", byID(t, got, "t2"))
	}
}

// An agent CLI rename is matched through its retext even after delivery, so
// the stale old-text snapshot entry does not duplicate the item.
func TestMergeMatchesAgentRetextAfterDelivery(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		seen(item("t1", "Run preprod --check --dry", domain.TodoPending, domain.AuthorAgent, mergeNow), "Run preprod --check", domain.TodoPending),
	}}
	rec := []Retext{{ID: "t1", OldText: Normalize("Run preprod --check"), At: mergeNow}}
	got := MergeWithRetexts(list, nil, rec, []domain.TodoSnapshotItem{snapItem("Run preprod --check", domain.TodoPending)}, delivered, mergeNow)
	if len(got.Items) != 1 {
		t.Fatalf("retext duplicated the item: %+v", got.Items)
	}
	if got.Items[0].Text != "Run preprod --check --dry" {
		t.Fatalf("CLI rename reverted: %+v", got.Items[0])
	}
}

// A reworded snapshot entry matches the last snapshot text, not only the
// stored text.
func TestMergeMatchesSeenText(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		seen(item("t1", "Operator's wording", domain.TodoPending, domain.AuthorUser, before), "agent's original wording", domain.TodoPending),
	}}
	got := Merge(list, nil, []domain.TodoSnapshotItem{snapItem("agent's original wording", domain.TodoDone)}, delivered, mergeNow)
	if len(got.Items) != 1 || got.Items[0].ID != "t1" {
		t.Fatalf("items = %+v", got.Items)
	}
	if got.Items[0].State != domain.TodoDone {
		t.Fatalf("agent's tick did not land: %+v", got.Items[0])
	}
}

// A mirror that only advances the merge base still persists.
func TestMirrorPersistsSeenOnlyChange(t *testing.T) {
	s := NewFiles(t.TempDir())
	if _, _, err := s.Mirror("s1", []domain.TodoSnapshotItem{snapItem("Write code", domain.TodoPending)}, delivered); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Mutate("s1", domain.AuthorAgent, func(l *domain.TodoList) error {
		l.Items[0].State = domain.TodoDone
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	prev, _ := s.Get("s1")
	l, ch, err := s.Mirror("s1", []domain.TodoSnapshotItem{snapItem("Write code", domain.TodoDone)}, delivered)
	if err != nil {
		t.Fatal(err)
	}
	// The snapshot agrees with the CLI state, so no item op — but the base
	// moved from pending to done and that must be written.
	if !ch.Empty() {
		t.Fatalf("unexpected ops: %+v", ch)
	}
	if l.Rev != prev.Rev+1 {
		t.Fatalf("seen-only change not persisted: rev %d -> %d", prev.Rev, l.Rev)
	}
	if l.Items[0].Seen == nil || l.Items[0].Seen.State != domain.TodoDone {
		t.Fatalf("Seen = %+v", l.Items[0].Seen)
	}
}

// A CLI write carries the merge base over by item ID, so a rebuilt item does
// not lose it.
func TestMutateCarriesSeenOverRebuild(t *testing.T) {
	s := NewFiles(t.TempDir())
	if _, _, err := s.Mirror("s1", []domain.TodoSnapshotItem{snapItem("Write code", domain.TodoPending)}, delivered); err != nil {
		t.Fatal(err)
	}
	l, _, err := s.Mutate("s1", domain.AuthorUser, func(l *domain.TodoList) error {
		l.Items[0] = domain.TodoItem{ID: l.Items[0].ID, Text: l.Items[0].Text, State: domain.TodoActive}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if l.Items[0].Seen == nil || l.Items[0].Seen.State != domain.TodoPending {
		t.Fatalf("Seen lost on rebuild: %+v", l.Items[0])
	}
}

// A pre-upgrade file (no mirror version) is migrated once: every item gets a
// merge base from its stored values and the write stamps the version.
func TestLoadMigratesPreUpgradeList(t *testing.T) {
	s := NewFiles(t.TempDir())
	path := s.root + "/s1.json"
	raw := `{"list":{"session_id":"s1","items":[{"id":"t1","text":"Write code","state":"pending","updated_by":"agent"}]},"next_id":2}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := s.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if l.Items[0].Seen == nil || l.Items[0].Seen.Text != "Write code" || l.Items[0].Seen.State != domain.TodoPending {
		t.Fatalf("migration did not seed Seen: %+v", l.Items[0])
	}
	if _, _, err := s.Mutate("s1", domain.AuthorUser, add("Deploy")); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d fileData
	if err := json.Unmarshal(stored, &d); err != nil {
		t.Fatal(err)
	}
	if d.MirrorVersion < mirrorVersionCurrent {
		t.Fatalf("mirror version not stamped: %d", d.MirrorVersion)
	}
	if d.List.Items[0].Seen == nil {
		t.Fatalf("migrated Seen not persisted: %+v", d.List.Items[0])
	}
}
