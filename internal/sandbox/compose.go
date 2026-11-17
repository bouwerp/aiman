package sandbox

import (
	"errors"

	"gopkg.in/yaml.v3"
)

// File is a compose document. Map keys marshal sorted, so equal input gives
// equal bytes.
type File struct {
	Name     string                    `yaml:"name"`
	Services map[string]Service        `yaml:"services"`
	Networks map[string]Network        `yaml:"networks,omitempty"`
	Volumes  map[string]map[string]any `yaml:"volumes"`
}

// Service is one compose service. Field order is the YAML key order.
type Service struct {
	Image       string            `yaml:"image"`
	PullPolicy  string            `yaml:"pull_policy,omitempty"`
	WorkingDir  string            `yaml:"working_dir,omitempty"`
	User        string            `yaml:"user,omitempty"`
	ReadOnly    bool              `yaml:"read_only"`
	CapDrop     []string          `yaml:"cap_drop"`
	CapAdd      []string          `yaml:"cap_add,omitempty"`
	SecurityOpt []string          `yaml:"security_opt,omitempty"`
	Tmpfs       []string          `yaml:"tmpfs,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty"`
	Volumes     []string          `yaml:"volumes,omitempty"`
	MemLimit    Memory            `yaml:"mem_limit"`
	CPUs        float64           `yaml:"cpus"`
	PidsLimit   int               `yaml:"pids_limit"`
	Ports       []string          `yaml:"ports,omitempty"`
	NetworkMode string            `yaml:"network_mode,omitempty"`
	// Networks is a []string, or a map of network name to Attach when a
	// network needs a fixed address.
	Networks    any                  `yaml:"networks,omitempty"`
	Sysctls     map[string]string    `yaml:"sysctls,omitempty"`
	Healthcheck *Healthcheck         `yaml:"healthcheck,omitempty"`
	Command     []string             `yaml:"command,omitempty"`
	DependsOn   map[string]Condition `yaml:"depends_on,omitempty"`
	Profiles    []string             `yaml:"profiles,omitempty"`
	Restart     string               `yaml:"restart,omitempty"`
}

// Condition is a depends_on entry.
type Condition struct {
	Condition string `yaml:"condition"`
}

// Healthcheck is a service's health check.
type Healthcheck struct {
	Test     []string `yaml:"test"`
	Interval string   `yaml:"interval"`
	Timeout  string   `yaml:"timeout"`
	Retries  int      `yaml:"retries"`
}

// Attach is one network of a service in the map form.
type Attach struct {
	IPv4Address string `yaml:"ipv4_address,omitempty"`
}

// Memory is a service's mem_limit: a compose size such as "2g" when Size is
// set, else a byte count.
type Memory struct {
	Bytes int64
	Size  string
}

// MemoryBytes is a mem_limit of n bytes.
func MemoryBytes(n int64) Memory { return Memory{Bytes: n} }

// MemorySize is a mem_limit written as a compose size such as "2g".
func MemorySize(s string) Memory { return Memory{Size: s} }

// MarshalYAML writes Size as a string, else Bytes as an integer.
func (m Memory) MarshalYAML() (any, error) {
	if m.Size != "" {
		return m.Size, nil
	}
	return m.Bytes, nil
}

// UnmarshalYAML reads an integer into Bytes and any other scalar into Size.
func (m *Memory) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return errors.New("sandbox: mem_limit must be a scalar")
	}
	var b int64
	if n.ShortTag() == "!!int" && n.Decode(&b) == nil {
		*m = Memory{Bytes: b}
		return nil
	}
	*m = Memory{Size: n.Value}
	return nil
}

// Limit sets a service's memory, CPU and process limits.
func Limit(s *Service, mem Memory, cpus float64, pids int) {
	s.MemLimit = mem
	s.CPUs = cpus
	s.PidsLimit = pids
}

// Marshal writes doc as YAML.
func Marshal(doc File) ([]byte, error) {
	return yaml.Marshal(doc)
}
