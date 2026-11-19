package agent

import (
	"regexp"
	"strings"
)

// claudeIDRe is an Anthropic model id: claude-<family>-<major>[-<minor>]
// with an optional -YYYYMMDD snapshot date.
var claudeIDRe = regexp.MustCompile(`^claude-([a-z]+)-(\d+)(?:-(\d{1,2}))?(?:-\d{8})?$`)

// ModelName is a short human name for a resolved model id: "Fable 5.2" for
// claude-fable-5-2, "Opus 5.5" for claude-opus-5-5. Any other id is
// returned as it is.
func ModelName(id string) string {
	id = strings.TrimSpace(id)
	fast := strings.HasSuffix(id, "@fast")
	id = strings.TrimSuffix(id, "@fast")
	m := claudeIDRe.FindStringSubmatch(id)
	if m == nil {
		if fast {
			return id + " fast"
		}
		return id
	}
	name := strings.ToUpper(m[1][:1]) + m[1][1:] + " " + m[2]
	if m[3] != "" {
		name += "." + m[3]
	}
	if fast {
		name += " fast"
	}
	return name
}

// ModelLabel is how a session's model reads when the launch value and what
// it resolved to may differ: "fable → Fable 5.2". An empty resolved id, or
// one equal to the launch value, is the launch value alone (or the name of
// the resolved id when there is no launch value).
func ModelLabel(launch, resolved string) string {
	launch, resolved = strings.TrimSpace(launch), strings.TrimSpace(resolved)
	switch {
	case resolved == "":
		return launch
	case launch == "":
		return ModelName(resolved)
	case strings.EqualFold(launch, resolved):
		return launch
	}
	return launch + " → " + ModelName(resolved)
}
