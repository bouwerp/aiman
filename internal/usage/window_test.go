package usage

import "testing"

func TestResolveWindowOrder(t *testing.T) {
	prev := windowTable
	t.Cleanup(func() { windowTable = prev })
	windowTable = map[string]confirmedWindow{
		"claude-opus-5-5": {Tokens: 1_000_000, Confirmed: true},
		"guessed-model":   {Tokens: 999_999, Confirmed: false},
	}

	if tokens, source := ResolveWindow("claude-opus-5-5", 123_456); tokens != 123_456 || source != "explicit" {
		t.Fatalf("explicit = %d %q, want 123456 explicit (table is 1000000)", tokens, source)
	}
	if tokens, source := ResolveWindow("claude-opus-5-5", 0); tokens != 1_000_000 || source != "table" {
		t.Fatalf("table = %d %q, want 1000000 table", tokens, source)
	}
	if tokens, source := ResolveWindow("no-such-model", 0); tokens != 200_000 || source != "fallback" {
		t.Fatalf("unknown = %d %q, want 200000 fallback", tokens, source)
	}
	if tokens, source := ResolveWindow("guessed-model", 0); tokens != 200_000 || source != "fallback" {
		t.Fatalf("unconfirmed = %d %q, want 200000 fallback", tokens, source)
	}
}
