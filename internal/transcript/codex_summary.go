package transcript

import (
	"encoding/json"
	"strings"
)

// toolSummaryMax bounds the one-line summary on a tool call. The full input
// stays in ToolCall.Input.
const toolSummaryMax = 200

// toolSummaryKeys are the arguments that best name what a call does, in order
// of preference, across the Codex and Pi tools.
var toolSummaryKeys = []string{"cmd", "command", "path", "file_path", "pattern", "query", "url"}

// summariseToolInput gives the one-line summary for a tool's input, which is
// JSON arguments or free text. Used by the Codex and Pi normalisers.
func summariseToolInput(input string) string {
	text := input
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(input), &args) == nil {
		text = ""
		for _, k := range toolSummaryKeys {
			var v string
			if json.Unmarshal(args[k], &v) == nil && v != "" {
				text = v
				break
			}
		}
	}
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	cut, _ := Truncate(first, toolSummaryMax)
	return cut
}
