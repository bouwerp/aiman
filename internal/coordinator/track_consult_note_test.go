package coordinator

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestConsultTriageNamesTheParentsPlace(t *testing.T) {
	if got := consultTriage(domain.Session{}); !strings.Contains(got, "Decisions table") {
		t.Fatalf("design session: %q", got)
	}
	got := consultTriage(domain.Session{ReviewPR: "o/r#1"})
	if strings.Contains(got, "Decisions table") || !strings.Contains(got, "review findings") {
		t.Fatalf("review session: %q", got)
	}
}
