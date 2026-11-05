package limits

import (
	"context"
	"regexp"
	"strconv"
)

const (
	deepSeekName = "deepseek"
	deepSeekURL  = "https://" + deepSeekHost + "/user/balance"
)

var currencyCode = regexp.MustCompile(`^[A-Za-z]{1,8}$`)

// NewDeepSeekSource reads the account balance per currency.
func NewDeepSeekSource(env Env) Source {
	return source{
		name:    deepSeekName,
		timeout: httpTimeout,
		read:    func(ctx context.Context) Reading { return readDeepSeek(ctx, env) },
	}
}

func readDeepSeek(ctx context.Context, env Env) Reading {
	key := deepSeekKey(env)
	if key == "" {
		return unavailableReading(deepSeekName)
	}
	var body struct {
		BalanceInfos []struct {
			Currency     string `json:"currency"`
			TotalBalance string `json:"total_balance"`
		} `json:"balance_infos"`
	}
	if reason := fetchJSON(ctx, env, deepSeekURL, key, &body); reason != "" {
		return unknownReading(deepSeekName, reason)
	}
	if len(body.BalanceInfos) == 0 {
		return unknownReading(deepSeekName, "no balance in the response")
	}
	balances := make([]Balance, 0, len(body.BalanceInfos))
	for _, info := range body.BalanceInfos {
		if _, err := strconv.ParseFloat(info.TotalBalance, 64); err != nil || !currencyCode.MatchString(info.Currency) {
			return unknownReading(deepSeekName, "unexpected response")
		}
		balances = append(balances, Balance{Amount: info.TotalBalance, Currency: info.Currency})
	}
	return Reading{
		Provider:   deepSeekName,
		Balances:   balances,
		State:      StateOK,
		ObservedAt: env.Now(),
		Source:     "GET /user/balance",
	}
}
