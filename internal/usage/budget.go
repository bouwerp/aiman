package usage

// Budget is one session's token budget against tokens already used.
// Tokens 0 means no budget. State is "" below 70%, "warn" at 70%,
// and "over" at 100%.
type Budget struct {
	Tokens int64   `json:"tokens"`
	Used   int64   `json:"used"`
	Pct    float64 `json:"pct"`
	State  string  `json:"state"`
}

// BudgetFor prices used against budget. A non-positive budget is none.
func BudgetFor(budget, used int64) Budget {
	if budget <= 0 {
		return Budget{Used: used}
	}
	b := Budget{Tokens: budget, Used: used, Pct: float64(used) / float64(budget)}
	b.State = budgetState(b.Pct)
	return b
}

func budgetState(pct float64) string {
	switch {
	case pct >= 1.0:
		return "over"
	case pct >= 0.70:
		return "warn"
	default:
		return ""
	}
}

func budgetRank(state string) int {
	switch state {
	case "warn":
		return 1
	case "over":
		return 2
	default:
		return 0
	}
}

// NextBudgetEmit reports the threshold to announce, if any, and the state
// to store. Each threshold fires once: a stored "warn" never warns again,
// and "over" ends the sequence.
func NextBudgetEmit(last string, budget, used int64) (emit, next string) {
	current := BudgetFor(budget, used).State
	if budgetRank(current) > budgetRank(last) {
		return current, current
	}
	if budgetRank(last) > budgetRank(current) {
		return "", last
	}
	return "", current
}
