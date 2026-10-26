package usage

import (
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

// Price is USD per million tokens.
// Confirmed false means the row is not a known price and is never used for cost.
// Estimated means the price is borrowed from a sibling model id; it is still used for cost.
type Price struct {
	Input        float64
	Output       float64
	CacheRead    float64
	CacheWrite5m float64
	CacheWrite1h float64
	Confirmed    bool
	Estimated    bool
}

// PriceTable maps a model id to its price.
type PriceTable map[string]Price

// DefaultPrices is the built-in USD-per-million table.
func DefaultPrices() PriceTable {
	// Claude rows come from the bundled claude-api skill pricing table cached
	// 2026-10-06; cache writes are 1.25× and 2× input. claude-opus-5-5@fast cache
	// columns assume the same 2× as input and output. claude-haiku-5-5 is the
	// ≤100K-token tier.
	//
	// Codex rows cite https://developers.openai.com/api/docs/pricing. Terra and
	// Luna are the 2026-07-30 reductions (Terra input 2, Luna input 0.20). Sol's
	// 4/20 is the promo from 2026-08-21, at least through 2026-11-21. The
	// 30-minute write sits in CacheWrite5m because the later Codex parser stores
	// writes in that field. The 12-hour write sits in CacheWrite1h.
	// codex-auto-review copies Terra; OpenAI publishes no row for it.
	return PriceTable{
		"claude-fable-5-1":     {Input: 10, Output: 50, CacheRead: 0.25, CacheWrite5m: 12.5, CacheWrite1h: 20, Confirmed: true},
		"claude-fable-5":       {Input: 10, Output: 50, CacheRead: 1, CacheWrite5m: 12.5, CacheWrite1h: 20, Confirmed: true},
		"claude-opus-5-5":      {Input: 4, Output: 20, CacheRead: 0.20, CacheWrite5m: 5, CacheWrite1h: 8, Confirmed: true},
		"claude-opus-5-5@fast": {Input: 8, Output: 40, CacheRead: 0.40, CacheWrite5m: 10, CacheWrite1h: 16, Confirmed: true},
		"claude-opus-5":        {Input: 5, Output: 25, CacheRead: 0.50, CacheWrite5m: 6.25, CacheWrite1h: 10, Confirmed: true},
		"claude-opus-4-8":      {Input: 5, Output: 25, CacheRead: 0.50, CacheWrite5m: 6.25, CacheWrite1h: 10, Confirmed: true},
		"claude-opus-4-7":      {Input: 5, Output: 25, CacheRead: 0.50, CacheWrite5m: 6.25, CacheWrite1h: 10, Confirmed: true},
		"claude-opus-4-6":      {Input: 5, Output: 25, CacheRead: 0.50, CacheWrite5m: 6.25, CacheWrite1h: 10, Confirmed: true},
		"claude-sonnet-5-5":    {Input: 2, Output: 10, CacheRead: 0.20, CacheWrite5m: 2.5, CacheWrite1h: 4, Confirmed: true},
		"claude-sonnet-5":      {Input: 2, Output: 10, CacheRead: 0.20, CacheWrite5m: 2.5, CacheWrite1h: 4, Confirmed: true},
		"claude-sonnet-4-6":    {Input: 3, Output: 15, CacheRead: 0.30, CacheWrite5m: 3.75, CacheWrite1h: 6, Confirmed: true},
		"claude-haiku-5-5":     {Input: 0.10, Output: 0.50, CacheRead: 0.01, CacheWrite5m: 0.125, CacheWrite1h: 0.20, Confirmed: true},
		"claude-haiku-4-5":     {Input: 1, Output: 5, CacheRead: 0.10, CacheWrite5m: 1.25, CacheWrite1h: 2, Confirmed: true},
		"gpt-5.6-sol":          {Input: 4, Output: 20, CacheRead: 0.40, CacheWrite5m: 5, CacheWrite1h: 10, Confirmed: true},
		"gpt-5.6-terra":        {Input: 2, Output: 12, CacheRead: 0.20, CacheWrite5m: 2.50, CacheWrite1h: 5, Confirmed: true},
		"gpt-5.6-luna":         {Input: 0.20, Output: 1.20, CacheRead: 0.02, CacheWrite5m: 0.25, CacheWrite1h: 0.50, Confirmed: true},
		"codex-auto-review":    {Input: 2, Output: 12, CacheRead: 0.20, CacheWrite5m: 2.50, CacheWrite1h: 5, Confirmed: true, Estimated: true},
	}
}

// Merge returns a new table. Keys in over replace keys in p.
func (p PriceTable) Merge(over PriceTable) PriceTable {
	out := make(PriceTable, len(p)+len(over))
	for k, v := range p {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// Lookup returns the price for model.
// An exact key wins. Otherwise, for an id that does not contain @, the longest
// key k such that model has prefix k+"-" wins. Ids that contain @ match only
// exactly. Missing and unconfirmed rows return false.
func (p PriceTable) Lookup(model string) (Price, bool) {
	if price, ok := p[model]; ok {
		if !price.Confirmed {
			return Price{}, false
		}
		return price, true
	}
	if strings.Contains(model, "@") {
		return Price{}, false
	}
	var (
		bestKey string
		best    Price
		hit     bool
	)
	for k, price := range p {
		if !strings.HasPrefix(model, k+"-") || len(k) <= len(bestKey) {
			continue
		}
		bestKey = k
		best = price
		hit = true
	}
	if !hit || !best.Confirmed {
		return Price{}, false
	}
	return best, true
}

// Cost is the sum of each token count times its USD-per-million price, divided by 1e6.
func (p Price) Cost(t domain.TokenCounts) float64 {
	sum := float64(t.Input)*p.Input +
		float64(t.Output)*p.Output +
		float64(t.CacheRead)*p.CacheRead +
		float64(t.CacheWrite5m)*p.CacheWrite5m +
		float64(t.CacheWrite1h)*p.CacheWrite1h
	return sum / 1e6
}
