package limits

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	openRouterName = "openrouter"
	openRouterURL  = "https://openrouter.ai/api/v1/key"
	// amountUnlimited is a key with no credit limit.
	amountUnlimited = "unlimited"
)

// NewOpenRouterSource reads credits left and recent use for the host's key.
func NewOpenRouterSource(env Env) Source {
	return source{
		name:    openRouterName,
		timeout: httpTimeout,
		read:    func(ctx context.Context) Reading { return readOpenRouter(ctx, env) },
	}
}

func readOpenRouter(ctx context.Context, env Env) Reading {
	key := openRouterKey(env)
	if key == "" {
		return unavailableReading(openRouterName)
	}
	var body struct {
		Data struct {
			// A raw value tells null (no limit on the key) from a field that is
			// gone, which must not read as unlimited.
			LimitRemaining json.RawMessage `json:"limit_remaining"`
			UsageDaily     *float64        `json:"usage_daily"`
			UsageWeekly    *float64        `json:"usage_weekly"`
		} `json:"data"`
	}
	if reason := fetchJSON(ctx, env, openRouterURL, key, &body); reason != "" {
		return unknownReading(openRouterName, reason)
	}
	balance, ok := openRouterBalance(body.Data.LimitRemaining)
	if !ok {
		return unknownReading(openRouterName, "unexpected response")
	}
	return Reading{
		Provider:   openRouterName,
		Balances:   []Balance{balance},
		Spend:      openRouterSpend(body.Data.UsageDaily, body.Data.UsageWeekly),
		State:      StateOK,
		ObservedAt: env.Now(),
		Source:     "GET /api/v1/key",
	}
}

func openRouterBalance(raw json.RawMessage) (Balance, bool) {
	if bytes.Equal(raw, []byte("null")) {
		return Balance{Amount: amountUnlimited}, true
	}
	left, err := strconv.ParseFloat(string(raw), 64)
	if err != nil {
		return Balance{}, false
	}
	return Balance{Amount: fmt.Sprintf("%.2f", left), Currency: "USD"}, true
}

func openRouterSpend(daily, weekly *float64) string {
	var parts []string
	if daily != nil {
		parts = append(parts, fmt.Sprintf("$%.2f today", *daily))
	}
	if weekly != nil {
		parts = append(parts, fmt.Sprintf("$%.2f this week", *weekly))
	}
	return strings.Join(parts, ", ")
}
