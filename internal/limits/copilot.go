package limits

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const (
	copilotName = "copilot"
	// ghExitAuth is gh's documented exit code for "authentication required".
	ghExitAuth = 4
)

var githubLogin = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)

// NewCopilotSource reads premium requests used this month through the gh login.
func NewCopilotSource(env Env) Source {
	return source{
		name:    copilotName,
		timeout: commandTimeout,
		read:    func(ctx context.Context) Reading { return readCopilot(ctx, env) },
	}
}

func readCopilot(ctx context.Context, env Env) Reading {
	login, exit, err := env.Run(ctx, "gh", "api", "user", "--jq", ".login")
	if exit == ghExitAuth {
		return unavailableReading(copilotName)
	}
	if failed, ok := commandFailure(ctx, copilotName, "gh", exit, err); ok {
		return failed
	}
	login = strings.TrimSpace(login)
	if !githubLogin.MatchString(login) {
		return unknownReading(copilotName, "unexpected login")
	}
	out, exit, err := env.Run(ctx, "gh", "api", "/users/"+login+"/settings/billing/premium_request/usage")
	if failed, ok := commandFailure(ctx, copilotName, "gh", exit, err); ok {
		return failed
	}
	used, ok := copilotPremiumRequests(out)
	if !ok {
		return unknownReading(copilotName, "unexpected response")
	}
	return Reading{
		Provider:   copilotName,
		Spend:      strconv.FormatFloat(used, 'f', -1, 64) + " premium requests this month",
		State:      StateOK,
		ObservedAt: env.Now(),
		Source:     "gh api premium_request/usage",
	}
}

// copilotPremiumRequests sums the month's items. It needs usageItems present and
// every item to carry a quantity, so a changed response never reads as a figure.
func copilotPremiumRequests(out string) (float64, bool) {
	var body struct {
		UsageItems *[]struct {
			GrossQuantity *float64 `json:"grossQuantity"`
		} `json:"usageItems"`
	}
	if json.Unmarshal([]byte(out), &body) != nil || body.UsageItems == nil {
		return 0, false
	}
	var total float64
	for _, item := range *body.UsageItems {
		if item.GrossQuantity == nil {
			return 0, false
		}
		total += *item.GrossQuantity
	}
	return math.Round(total*100) / 100, true
}
