package domain

import "testing"

func TestValidBaseRef(t *testing.T) {
	for _, ref := range []string{"main", "origin/main", "HEAD", "HEAD~1", "HEAD^", "v1.2.3", "abc1234"} {
		if err := ValidBaseRef(ref); err != nil {
			t.Errorf("ValidBaseRef(%q) = %v, want nil", ref, err)
		}
	}
	for _, ref := range []string{"", "-x", "--upload-pack=x", "a..b", "a b", "a\nb", "a:b", "a*", `a\b`, "a\x7f"} {
		if err := ValidBaseRef(ref); err == nil {
			t.Errorf("ValidBaseRef(%q) = nil, want an error", ref)
		}
	}
}
