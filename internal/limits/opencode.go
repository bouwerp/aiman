package limits

import (
	"context"
	"regexp"
)

const openCodeName = "opencode"

var openCodeTotalCost = regexp.MustCompile(`Total Cost\s+(\$[0-9][0-9,]*(?:\.[0-9]+)?)`)

// NewOpenCodeSource reads what OpenCode spent in the last 7 days. This is spend,
// not a limit: OpenCode's limits exist only in its web console.
func NewOpenCodeSource(env Env) Source {
	return source{
		name:    openCodeName,
		timeout: commandTimeout,
		read:    func(ctx context.Context) Reading { return readOpenCode(ctx, env) },
	}
}

func readOpenCode(ctx context.Context, env Env) Reading {
	out, exit, err := env.Run(ctx, "opencode", "stats", "--days", "7")
	if failed, ok := commandFailure(ctx, openCodeName, "opencode", exit, err); ok {
		return failed
	}
	match := openCodeTotalCost.FindStringSubmatch(out)
	if match == nil {
		return unknownReading(openCodeName, "unexpected screen")
	}
	return Reading{
		Provider:   openCodeName,
		Spend:      match[1] + " in 7 days",
		State:      StateOK,
		ObservedAt: env.Now(),
		Source:     "opencode stats --days 7",
	}
}
