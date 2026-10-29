package design

import "testing"

func TestKey(t *testing.T) {
	cases := []struct{ issue, branch, want string }{
		{"PROJ-1", "x", "PROJ-1"},
		{"", "feat/a-b", "feat-a-b"},
		{" ", "a/b/c", "a-b-c"},
	}
	for _, c := range cases {
		if got := Key(c.issue, c.branch); got != c.want {
			t.Errorf("Key(%q,%q)=%q want %q", c.issue, c.branch, got, c.want)
		}
	}
}

func TestCountTriage(t *testing.T) {
	md := `# T

## Other

| Objection | Severity | Triage | Reason |
|---|---|---|---|
| x | high | accept | no |

## Decisions

| Objection | Severity | Triage | Reason |
|---|---|---|---|
| a | high | accept | r |
| b | low | Reject | r |
| c | low | defer | r |
| d | low | accept | r |
| e | low | maybe | r |

## After

| Objection | Severity | Triage | Reason |
|---|---|---|---|
| z | low | reject | r |
`
	a, r, d := CountTriage(md)
	if a != 2 || r != 1 || d != 1 {
		t.Fatalf("got %d %d %d", a, r, d)
	}
	if a, r, d := CountTriage("no table"); a+r+d != 0 {
		t.Fatalf("empty: %d %d %d", a, r, d)
	}
}
