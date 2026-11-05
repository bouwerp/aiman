package throwawayreview

import "testing"

func TestTotalEmpty(t *testing.T) {
	if got := Total(nil); got != 0 {
		t.Fatalf("Total(nil) = %d, want 0", got)
	}
}
