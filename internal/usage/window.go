package usage

// FallbackWindow is the context window, in tokens, when config is unset and
// the model has no confirmed row.
const FallbackWindow = 200_000

// confirmedWindow is one model's context window.
// Confirmed is false when the size is not a cited vendor figure, and then it
// is not used.
type confirmedWindow struct {
	Tokens    int
	Confirmed bool
}

// contextSample is the latest occupancy observed in one transcript read.
type contextSample struct {
	Tokens int64
	Model  string
	OK     bool
}

// windowTable is the confirmed context-window table, in tokens.
// ResolveWindow reads it only when the explicit config value is not positive.
// A row belongs here only with a vendor citation in the comment beside it,
// the same bar as DefaultPrices. No size is cited, so the table ships empty.
var windowTable = map[string]confirmedWindow{}

// ResolveWindow picks a context window for model.
// A positive explicit value is usage.context_window_tokens and wins without
// reading the table. Otherwise a confirmed table row wins. Anything else,
// including an unknown model and an unconfirmed row, is FallbackWindow with
// source "fallback".
func ResolveWindow(model string, explicit int) (int, string) {
	if explicit > 0 {
		return explicit, "explicit"
	}
	if row, ok := windowTable[model]; ok && row.Confirmed && row.Tokens > 0 {
		return row.Tokens, "table"
	}
	return FallbackWindow, "fallback"
}
