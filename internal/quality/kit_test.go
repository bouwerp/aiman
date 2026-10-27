package quality

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

var kitLists = []domain.Checklist{
	{Name: "go", Path: "/wt/.aiman/checklists/go.md", Source: SourceRepo},
	{Name: "tf", Path: "/home/u/.aiman/checklists/tf.md", Source: SourceUser},
}

const taskFixture = "<!--\nDO NOT COMMIT\n-->\n\n---\n\n# Task\n\n## PROJ-1: Thing\n\n### Description\n\nDo it.\n\n---\n\n# Working Guidelines\n\nBe good.\n"

func TestDoneWhenMergesAndDedupes(t *testing.T) {
	got := DoneWhen([]string{"Tests added", "Docs updated", "  "}, []string{"tests  added.", "`make ci` green", "`Make CI` green."})
	want := []string{"Tests added", "Docs updated", "`make ci` green"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DoneWhen = %#v, want %#v", got, want)
	}
	if got := DoneWhen(nil, nil); got != nil {
		t.Errorf("DoneWhen(nil, nil) = %#v, want nil", got)
	}
}

func TestRenderBlocksDeterministic(t *testing.T) {
	conds := []string{"Tests added", "Docs updated"}
	a, b := RenderDoneWhenBlock(conds), RenderDoneWhenBlock(conds)
	if a != b {
		t.Fatalf("RenderDoneWhenBlock not deterministic")
	}
	for _, want := range []string{"<!-- aiman:done-when -->\n## Done when\n", "- [ ] Docs updated\n", "`" + DoneItemText + "`", "<!-- /aiman:done-when -->\n"} {
		if !strings.Contains(a, want) {
			t.Errorf("done-when block missing %q:\n%s", want, a)
		}
	}
	c := RenderChecklistBlock(kitLists)
	if c != RenderChecklistBlock(kitLists) {
		t.Fatalf("RenderChecklistBlock not deterministic")
	}
	for _, want := range []string{"<!-- aiman:checklists -->\n## Checklists\n", "- /wt/.aiman/checklists/go.md\n", "<!-- /aiman:checklists -->\n"} {
		if !strings.Contains(c, want) {
			t.Errorf("checklist block missing %q:\n%s", want, c)
		}
	}
	if RenderDoneWhenBlock(nil) != "" || RenderChecklistBlock(nil) != "" {
		t.Errorf("empty input must render nothing")
	}
}

func TestReplaceKitBlocksKeepsDoneWhen(t *testing.T) {
	dw := RenderDoneWhenBlock([]string{"Tests added"})
	task := ReplaceKitBlocks(taskFixture, &dw, RenderChecklistBlock(kitLists[:1]))

	got := ReplaceKitBlocks(task, nil, RenderChecklistBlock(kitLists))
	if !strings.Contains(got, dw) {
		t.Fatalf("Done-when block changed:\n%s", got)
	}
	if !strings.Contains(got, "- /home/u/.aiman/checklists/tf.md") {
		t.Errorf("checklist block not replaced:\n%s", got)
	}
	if strings.Count(got, "<!-- aiman:checklists -->") != 1 || strings.Count(got, "<!-- aiman:done-when -->") != 1 {
		t.Errorf("blocks duplicated:\n%s", got)
	}
	if again := ReplaceKitBlocks(got, nil, RenderChecklistBlock(kitLists)); again != got {
		t.Errorf("unchanged input rewrote the task:\n%s", again)
	}
	if removed := ReplaceKitBlocks(got, nil, ""); strings.Contains(removed, "aiman:checklists") || !strings.Contains(removed, dw) {
		t.Errorf("empty checklists must remove only the checklist block:\n%s", removed)
	}
}

func TestReplaceKitBlocksInsertsAfterTask(t *testing.T) {
	dw := RenderDoneWhenBlock([]string{"Tests added"})
	cl := RenderChecklistBlock(kitLists)
	got := ReplaceKitBlocks(taskFixture, &dw, cl)

	iDesc := strings.Index(got, "Do it.")
	iDone := strings.Index(got, "<!-- aiman:done-when -->")
	iList := strings.Index(got, "<!-- aiman:checklists -->")
	iGuide := strings.Index(got, "# Working Guidelines")
	if iDesc >= iDone || iDone >= iList || iList >= iGuide {
		t.Fatalf("order wrong (desc %d, done %d, lists %d, guidelines %d):\n%s", iDesc, iDone, iList, iGuide, got)
	}
	if !strings.Contains(got, "<!-- /aiman:checklists -->\n\n---\n\n# Working Guidelines") {
		t.Errorf("blocks not placed before the section rule:\n%s", got)
	}

	noTask := ReplaceKitBlocks("# Session kit\n", &dw, cl)
	if !strings.HasPrefix(noTask, "# Session kit\n\n"+dw+"\n"+cl) {
		t.Errorf("without a Task section blocks must be appended:\n%s", noTask)
	}

	onlyLists := ReplaceKitBlocks(taskFixture, nil, cl)
	withDone := ReplaceKitBlocks(onlyLists, &dw, cl)
	if strings.Index(withDone, "aiman:done-when") > strings.Index(withDone, "aiman:checklists") {
		t.Errorf("Done-when must be inserted before an existing checklist block:\n%s", withDone)
	}
}

func TestParseTaskKitRoundTrip(t *testing.T) {
	conds := []string{"Tests added", "`make ci` green"}
	dw := RenderDoneWhenBlock(conds)
	task := ReplaceKitBlocks(taskFixture, &dw, RenderChecklistBlock(kitLists))
	gotConds, gotPaths := ParseTaskKit(task)
	if !reflect.DeepEqual(gotConds, conds) {
		t.Errorf("conditions = %#v, want %#v", gotConds, conds)
	}
	wantPaths := []string{kitLists[0].Path, kitLists[1].Path}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Errorf("paths = %#v, want %#v", gotPaths, wantPaths)
	}
}

func TestParseTaskKitWithoutBlocks(t *testing.T) {
	conds, paths := ParseTaskKit(taskFixture)
	if conds != nil || paths != nil {
		t.Errorf("ParseTaskKit = %#v, %#v; want nil, nil", conds, paths)
	}
}

func TestKitOnlyTaskFile(t *testing.T) {
	dw := RenderDoneWhenBlock([]string{"Tests added"})
	cl := RenderChecklistBlock(kitLists)
	got := KitOnlyTaskFile(dw, cl)
	for _, want := range []string{"DO NOT COMMIT", "# Session kit\n", dw, cl} {
		if !strings.Contains(got, want) {
			t.Errorf("kit-only file missing %q:\n%s", want, got)
		}
	}
	conds, paths := ParseTaskKit(got)
	if len(conds) != 1 || len(paths) != 2 {
		t.Errorf("ParseTaskKit(kit-only) = %v, %v", conds, paths)
	}
}
