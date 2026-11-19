package aimanskill

import (
	"strings"
	"testing"
)

// TestSkillAllowsOnlyAimanSlack rules out every other Slack integration, in
// the description an agent sees first and in the Slack section, and sends a
// failure to the operator instead of a fallback.
func TestSkillAllowsOnlyAimanSlack(t *testing.T) {
	head, _, _ := strings.Cut(Text, "\n---\n")
	if !strings.Contains(head, "only through aiman slack, never another Slack integration") {
		t.Fatalf("description does not rule out other Slack integrations:\n%s", head)
	}
	s := section(t, "## Slack")
	for _, want := range []string{"Do not use any other Slack integration", "even when your tools list one", "stop and tell the operator"} {
		if !strings.Contains(s, want) {
			t.Fatalf("Slack section missing %q", want)
		}
	}
	if strings.Contains(s, "Fall back to your own Slack integration") {
		t.Fatal("Slack section still allows a fallback to another integration")
	}
}
