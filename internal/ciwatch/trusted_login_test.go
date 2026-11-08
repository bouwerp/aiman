package ciwatch

import "testing"

func TestTrustedLoginMatch(t *testing.T) {
	w := New(Deps{TrustedLogins: []string{" Review-Bot ", ""}})
	for author, want := range map[string]bool{
		"review-bot": true,
		"REVIEW-BOT": true,
		"review":     false,
		"":           false,
	} {
		if got := w.trustedLogin(author); got != want {
			t.Errorf("trustedLogin(%q) = %v, want %v", author, got, want)
		}
	}
}
