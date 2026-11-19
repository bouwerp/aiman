package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usage"
)

// TestMetaLineShowsResolvedModel names what an alias resolved to, from the
// latest turn usage.get reports, and drops it when a report has none.
func TestMetaLineShowsResolvedModel(t *testing.T) {
	s := domain.Session{ID: "s1", AgentName: "Claude Code", AgentModel: "fable"}
	x := applyUsageToExtras(detailMetaExtras{}, usage.Report{Known: true, Model: "claude-fable-5-2"})
	if got := ansi.Strip(renderMetaLine(s, x, time.Now(), 200)); !strings.Contains(got, "Claude Code fable → Fable 5.2") {
		t.Fatalf("meta = %q", got)
	}
	x = applyUsageToExtras(x, usage.Report{Known: true})
	if got := ansi.Strip(renderMetaLine(s, x, time.Now(), 200)); strings.Contains(got, "→") || !strings.Contains(got, "Claude Code fable") {
		t.Fatalf("meta after a report with no model = %q", got)
	}
}
