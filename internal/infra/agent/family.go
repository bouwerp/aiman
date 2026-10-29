package agent

import "strings"

// Family names used by Family. They are also the provider segment a model
// id may carry (anthropic/claude-…), which is why one list serves both.
const (
	FamilyAnthropic = "anthropic"
	FamilyOpenAI    = "openai"
	FamilyGoogle    = "google"
	FamilyXAI       = "xai"
	FamilyDeepSeek  = "deepseek"
)

// binaryFamilies maps single-vendor agent binaries to their model family.
// Multi-model agents (pi, copilot, cursor-agent, …) are absent: their family
// comes from the model id.
var binaryFamilies = map[string]string{
	"claude": FamilyAnthropic,
	"codex":  FamilyOpenAI,
	"grok":   FamilyXAI,
	"agy":    FamilyGoogle,
}

// modelPrefixes maps lowercase model-id prefixes to a family. No entry is a
// prefix of another, so the first match is the only match.
var modelPrefixes = []struct {
	prefix string
	family string
}{
	{"claude", FamilyAnthropic},
	{"opus", FamilyAnthropic},
	{"sonnet", FamilyAnthropic},
	{"haiku", FamilyAnthropic},
	{"gpt", FamilyOpenAI},
	{"o3", FamilyOpenAI},
	{"o4", FamilyOpenAI},
	{"codex", FamilyOpenAI},
	{"gemini", FamilyGoogle},
	{"grok", FamilyXAI},
	{"deepseek", FamilyDeepSeek},
}

// Family returns the model family ("anthropic", "openai", "google", "xai",
// "deepseek") an agent command and model belong to, or "" when neither
// identifies one. The binary decides first; the model decides for
// multi-model agents.
func Family(command, model string) string {
	base := CommandBase(command)
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if f, ok := binaryFamilies[base]; ok {
		return f
	}
	return modelFamily(model)
}

// modelFamily classifies a model id such as "anthropic/claude-opus-4-7:high".
// A provider segment that names a family wins. Otherwise the last path
// segment is matched by prefix, so "openrouter/anthropic/claude-x" resolves
// through "claude-x".
func modelFamily(model string) string {
	id := strings.ToLower(strings.TrimSpace(model))
	id, _, _ = strings.Cut(id, ":") // drop the ":<thinking>" suffix
	if provider, _, ok := strings.Cut(id, "/"); ok && isFamily(provider) {
		return provider
	}
	id = id[strings.LastIndex(id, "/")+1:]
	for _, p := range modelPrefixes {
		if strings.HasPrefix(id, p.prefix) {
			return p.family
		}
	}
	return ""
}

func isFamily(name string) bool {
	switch name {
	case FamilyAnthropic, FamilyOpenAI, FamilyGoogle, FamilyXAI, FamilyDeepSeek:
		return true
	}
	return false
}
