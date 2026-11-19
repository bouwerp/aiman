package agenthook

import "path/filepath"

// SlackGuardMarker identifies the generated Slack guard script.
const SlackGuardMarker = "slack-guard.sh"

// slackGuardMatcher matches Claude's Slack MCP tools: a Slack plugin
// (mcp__plugin_slack_slack__…) and the claude.ai connector
// (mcp__claude_ai_Slack__…).
const slackGuardMatcher = "mcp__.*[Ss]lack.*"

func slackGuardPath(home string) string {
	return filepath.Join(home, ".aiman", "hooks", SlackGuardMarker)
}

// slackGuardScript refuses a Slack MCP tool inside an Aiman session (exit 2,
// the reason on stderr for the agent) and lets it through anywhere else.
// Slack in a session goes through `aiman slack`, which acts as the member
// who approved the Aiman app and honours slack.writes.
const slackGuardScript = `#!/bin/sh
[ "${AIMAN_ENV:-}" = 1 ] || exit 0
echo "In an Aiman session, use aiman slack for Slack (the aiman skill's Slack section), for example aiman slack search, aiman slack history, aiman slack thread, aiman slack post. Do not use another Slack integration. If aiman slack cannot do this, tell the operator the command you ran and its error." >&2
exit 2
`
