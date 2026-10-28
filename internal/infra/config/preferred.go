package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// PreferredAgent is one agents.preferred entry: an agent command, optionally
// with the model and effort a child started from this entry runs with. Empty
// Model or Effort leaves that setting to agent_defaults and the repo's
// agent_overrides.
//
// In YAML an entry is a plain string (just the command) or a mapping with
// agent, model and effort keys. An entry with only Agent set is written back
// as a plain string, so a list of strings round-trips unchanged.
type PreferredAgent struct {
	Agent  string `yaml:"agent"`
	Model  string `yaml:"model,omitempty"`
	Effort string `yaml:"effort,omitempty"`
}

// preferredAgentFields is PreferredAgent without its YAML methods.
type preferredAgentFields PreferredAgent

// MarshalYAML writes a bare command as a string and anything else as a mapping.
func (p PreferredAgent) MarshalYAML() (any, error) {
	if p.Model == "" && p.Effort == "" {
		return p.Agent, nil
	}
	return preferredAgentFields(p), nil
}

// UnmarshalYAML accepts a string command or an agent/model/effort mapping.
func (p *PreferredAgent) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*p = PreferredAgent{Agent: n.Value}
		return nil
	case yaml.MappingNode:
		var f preferredAgentFields
		if err := n.Decode(&f); err != nil {
			return err
		}
		*p = PreferredAgent(f)
		return nil
	default:
		return fmt.Errorf("line %d: agents.preferred entry must be a command or an agent/model/effort mapping", n.Line)
	}
}

// Command is the trimmed agent command.
func (p PreferredAgent) Command() string { return strings.TrimSpace(p.Agent) }

// Label is the entry as one row: command, model and effort separated by two
// spaces. A missing model reads "(default model)" when an effort follows it.
func (p PreferredAgent) Label() string {
	parts := []string{p.Command()}
	model, effort := strings.TrimSpace(p.Model), strings.TrimSpace(p.Effort)
	switch {
	case model != "":
		parts = append(parts, model)
	case effort != "":
		parts = append(parts, "(default model)")
	}
	if effort != "" {
		parts = append(parts, effort)
	}
	return strings.Join(parts, "  ")
}

// PreferredFromCommands builds bare entries for the given commands.
func PreferredFromCommands(commands ...string) []PreferredAgent {
	out := make([]PreferredAgent, 0, len(commands))
	for _, c := range commands {
		out = append(out, PreferredAgent{Agent: c})
	}
	return out
}

// PreferredCommands lists each entry's command in order, repeats included.
func (a AgentsConfig) PreferredCommands() []string {
	if a.Preferred == nil {
		return nil
	}
	out := make([]string, 0, len(a.Preferred))
	for _, p := range a.Preferred {
		out = append(out, p.Command())
	}
	return out
}
