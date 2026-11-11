package ciwatch

// Acceptance tests for ci-minutes piece C (docs/specs/ci-minutes.md,
// Behaviour 3): one steering note per finished CI run.

import (
	"strings"
	"testing"
)

// wantRunNoteEnd ends every failed-run note.
const wantRunNoteEnd = "Fix every failed check, run aiman ci local --wait, then push once. Pushes are refused until the gate passes for the new commit."

func twoFailures(sha string, extra ...Check) PRView {
	checks := []Check{
		{Name: "lint", Conclusion: ConclusionFailure, DetailsURL: "https://github.com/o/r/actions/runs/111/job/222", JobID: 222},
		{Name: "unit", Conclusion: ConclusionFailure, DetailsURL: "https://github.com/o/r/actions/runs/111/job/333", JobID: 333},
		{Name: "build", Conclusion: ConclusionSuccess},
	}
	return PRView{Number: 42, HeadSHA: sha, State: "OPEN", Checks: append(checks, extra...)}
}

func TestOneNotePerRunListsEveryFailedCheck(t *testing.T) {
	r := newRig(t)
	r.gh.logs[222] = "lint broke"
	r.gh.logs[333] = "unit broke"
	r.gh.setPR(twoFailures("aaaaaaa1"))
	r.poll(t, testSession())

	notes := r.steers.all()
	if len(notes) != 1 {
		t.Fatalf("notes %d, want one for the run: %q", len(notes), notes)
	}
	note := notes[0]
	for _, want := range []string{`"lint"`, `"unit"`, "lint broke", "unit broke", "aaaaaaa"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, `"build"`) {
		t.Fatalf("note names the passing check:\n%s", note)
	}
	if !strings.HasSuffix(strings.TrimSpace(note), wantRunNoteEnd) {
		t.Fatalf("note does not end with the run instruction:\n%s", note)
	}
	items := r.todos.items()
	if len(items) != 2 || items[0].Ref != "check:lint" || items[1].Ref != "check:unit" {
		t.Fatalf("todos %+v, want one per failed check", items)
	}

	r.poll(t, testSession())
	if n := len(r.steers.all()); n != 1 {
		t.Fatalf("same run polled again: %d notes, want 1", n)
	}
}

func TestOneNotePerRunWaitsForTheRunToFinish(t *testing.T) {
	r := newRig(t)
	r.gh.setPR(twoFailures("aaaaaaa1", Check{Name: "e2e", Conclusion: ConclusionPending}))
	r.poll(t, testSession())
	if n := len(r.steers.all()); n != 0 {
		t.Fatalf("notes while e2e still runs: %d, want 0", n)
	}

	r.gh.setPR(twoFailures("aaaaaaa1", Check{Name: "e2e", Conclusion: ConclusionFailure}))
	r.poll(t, testSession())
	notes := r.steers.all()
	if len(notes) != 1 {
		t.Fatalf("notes after the run finished: %d, want 1", len(notes))
	}
	for _, name := range []string{`"lint"`, `"unit"`, `"e2e"`} {
		if !strings.Contains(notes[0], name) {
			t.Fatalf("note lacks %s:\n%s", name, notes[0])
		}
	}
}
