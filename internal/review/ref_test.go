package review

import "testing"

func TestParseRef(t *testing.T) {
	good, err := ParseRef("acme/widgets#12")
	if err != nil {
		t.Fatal(err)
	}
	if good != (Ref{Owner: "acme", Repo: "widgets", Number: 12}) {
		t.Fatalf("got %+v", good)
	}
	if good.String() != "acme/widgets#12" || good.Slug() != "acme/widgets" {
		t.Fatalf("String/Slug: %q %q", good.String(), good.Slug())
	}
	for _, bad := range []string{"", "acme/widgets", "acme/widgets#x", "acme/widgets#0", "acme/widgets#-1", "a/b/c#1", "acme#1", "/b#1", "a/#1", "#1"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) accepted", bad)
		}
	}
}
