package usage

import (
	"math"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestDefaultPricesMatchPublished(t *testing.T) {
	got := DefaultPrices()
	want := []struct {
		key       string
		price     Price
		estimated bool
	}{
		{"claude-fable-5-1", Price{Input: 10, Output: 50, CacheRead: 0.25, CacheWrite5m: 12.5, CacheWrite1h: 20, Confirmed: true}, false},
		{"claude-fable-5", Price{Input: 10, Output: 50, CacheRead: 1, CacheWrite5m: 12.5, CacheWrite1h: 20, Confirmed: true}, false},
		{"claude-opus-5-5", Price{Input: 4, Output: 20, CacheRead: 0.20, CacheWrite5m: 5, CacheWrite1h: 8, Confirmed: true}, false},
		{"claude-opus-5-5@fast", Price{Input: 8, Output: 40, CacheRead: 0.40, CacheWrite5m: 10, CacheWrite1h: 16, Confirmed: true}, false},
		{"claude-opus-5", Price{Input: 5, Output: 25, CacheRead: 0.50, CacheWrite5m: 6.25, CacheWrite1h: 10, Confirmed: true}, false},
		{"claude-opus-4-8", Price{Input: 5, Output: 25, CacheRead: 0.50, CacheWrite5m: 6.25, CacheWrite1h: 10, Confirmed: true}, false},
		{"claude-opus-4-7", Price{Input: 5, Output: 25, CacheRead: 0.50, CacheWrite5m: 6.25, CacheWrite1h: 10, Confirmed: true}, false},
		{"claude-opus-4-6", Price{Input: 5, Output: 25, CacheRead: 0.50, CacheWrite5m: 6.25, CacheWrite1h: 10, Confirmed: true}, false},
		{"claude-sonnet-5-5", Price{Input: 2, Output: 10, CacheRead: 0.20, CacheWrite5m: 2.5, CacheWrite1h: 4, Confirmed: true}, false},
		{"claude-sonnet-5", Price{Input: 2, Output: 10, CacheRead: 0.20, CacheWrite5m: 2.5, CacheWrite1h: 4, Confirmed: true}, false},
		{"claude-sonnet-4-6", Price{Input: 3, Output: 15, CacheRead: 0.30, CacheWrite5m: 3.75, CacheWrite1h: 6, Confirmed: true}, false},
		{"claude-haiku-5-5", Price{Input: 0.10, Output: 0.50, CacheRead: 0.01, CacheWrite5m: 0.125, CacheWrite1h: 0.20, Confirmed: true}, false},
		{"claude-haiku-4-5", Price{Input: 1, Output: 5, CacheRead: 0.10, CacheWrite5m: 1.25, CacheWrite1h: 2, Confirmed: true}, false},
		{"gpt-5.6-sol", Price{Input: 4, Output: 20, CacheRead: 0.40, CacheWrite5m: 5, CacheWrite1h: 10, Confirmed: true}, false},
		{"gpt-5.6-terra", Price{Input: 2, Output: 12, CacheRead: 0.20, CacheWrite5m: 2.50, CacheWrite1h: 5, Confirmed: true}, false},
		{"gpt-5.6-luna", Price{Input: 0.20, Output: 1.20, CacheRead: 0.02, CacheWrite5m: 0.25, CacheWrite1h: 0.50, Confirmed: true}, false},
		{"codex-auto-review", Price{Input: 2, Output: 12, CacheRead: 0.20, CacheWrite5m: 2.50, CacheWrite1h: 5, Confirmed: true, Estimated: true}, true},
	}
	if len(got) != len(want) {
		t.Fatalf("DefaultPrices() has %d keys, want %d", len(got), len(want))
	}
	for _, tt := range want {
		t.Run(tt.key, func(t *testing.T) {
			p, ok := got[tt.key]
			if !ok {
				t.Fatal("missing")
			}
			if !priceNear(p, tt.price) || p.Confirmed != tt.price.Confirmed || p.Estimated != tt.estimated {
				t.Fatalf("got %+v, want %+v", p, tt.price)
			}
		})
	}
}

func TestLookupPrefix(t *testing.T) {
	p := DefaultPrices()
	got, ok := p.Lookup("claude-haiku-4-5-20251001")
	if !ok {
		t.Fatal("Lookup(claude-haiku-4-5-20251001) ok = false")
	}
	if !priceNear(got, p["claude-haiku-4-5"]) {
		t.Fatalf("dated haiku = %+v, want %+v", got, p["claude-haiku-4-5"])
	}
	got, ok = p.Lookup("claude-opus-5")
	if !ok {
		t.Fatal("Lookup(claude-opus-5) ok = false")
	}
	if !priceNear(got, p["claude-opus-5"]) || priceNear(got, p["claude-opus-5-5"]) {
		t.Fatalf("Lookup(claude-opus-5) = %+v, want opus-5 %+v", got, p["claude-opus-5"])
	}
}

func TestLookupFast(t *testing.T) {
	p := DefaultPrices()
	got, ok := p.Lookup("claude-opus-5-5@fast")
	if !ok {
		t.Fatal("Lookup(claude-opus-5-5@fast) ok = false")
	}
	if got.Input != 8 || got.Output != 40 {
		t.Fatalf("Lookup(claude-opus-5-5@fast) = %+v, want input 8 output 40", got)
	}
	if _, ok := p.Lookup("claude-sonnet-5@fast"); ok {
		t.Fatal("Lookup(claude-sonnet-5@fast) ok = true, want false")
	}
}

func TestCostSkipsUnconfirmedPrice(t *testing.T) {
	table := DefaultPrices()
	terra, ok := table.Lookup("gpt-5.6-terra")
	if !ok || terra.Input != 2 || terra.Output != 12 || !terra.Confirmed {
		t.Fatalf("Lookup(gpt-5.6-terra) = %+v, %v; want confirmed terra", terra, ok)
	}
	const unpriced = "custom-unpriced"
	table[unpriced] = Price{Input: 9, Output: 9, Confirmed: false}
	if _, ok := table.Lookup(unpriced); ok {
		t.Fatal("Lookup(unconfirmed) ok = true, want false")
	}
}

func TestCostOpusFixture(t *testing.T) {
	got := DefaultPrices()["claude-opus-5-5"].Cost(domain.TokenCounts{
		Input: 4, Output: 325, CacheRead: 97159, CacheWrite1h: 36005,
	})
	const want = 0.3139878
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("Cost() = %.10f, want %.10f", got, want)
	}
}

func TestMergeOverrides(t *testing.T) {
	before := DefaultPrices()
	opus := before["claude-opus-5-5"]
	haiku := before["claude-haiku-4-5"]
	merged := before.Merge(PriceTable{
		"claude-opus-5-5": {Input: 1, Confirmed: true},
	})
	wantOpus := Price{Input: 1, Confirmed: true}
	if !priceNear(merged["claude-opus-5-5"], wantOpus) || merged["claude-opus-5-5"].Confirmed != true || merged["claude-opus-5-5"].Estimated {
		t.Fatalf("merged opus = %+v, want %+v", merged["claude-opus-5-5"], wantOpus)
	}
	if !priceNear(merged["claude-haiku-4-5"], haiku) {
		t.Fatalf("merged haiku = %+v, want %+v", merged["claude-haiku-4-5"], haiku)
	}
	if len(merged) != len(before) {
		t.Fatalf("merged len = %d, want %d", len(merged), len(before))
	}
	if !priceNear(before["claude-opus-5-5"], opus) {
		t.Fatalf("Merge mutated receiver opus to %+v", before["claude-opus-5-5"])
	}
	if !priceNear(DefaultPrices()["claude-opus-5-5"], opus) {
		t.Fatalf("DefaultPrices() opus changed to %+v", DefaultPrices()["claude-opus-5-5"])
	}
}

func priceNear(got, want Price) bool {
	const eps = 1e-12
	return math.Abs(got.Input-want.Input) < eps &&
		math.Abs(got.Output-want.Output) < eps &&
		math.Abs(got.CacheRead-want.CacheRead) < eps &&
		math.Abs(got.CacheWrite5m-want.CacheWrite5m) < eps &&
		math.Abs(got.CacheWrite1h-want.CacheWrite1h) < eps &&
		got.Confirmed == want.Confirmed &&
		got.Estimated == want.Estimated
}
