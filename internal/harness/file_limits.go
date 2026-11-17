package harness

import (
	"fmt"
	"path"
	"strings"

	"github.com/bouwerp/aiman/internal/cgroup"
)

// Default limits (design: Container profiles and limits).
const (
	DefaultAppMemory     int64 = 2 << 30
	DefaultServiceMemory int64 = 2 << 30
	DefaultRunMemory     int64 = 6 << 30
	DefaultCPUs                = 2.0
	DefaultPIDs                = 256
)

// ResolvedLimits are a container's limits with defaults filled: Memory in
// bytes, CPUs as a fraction of cores, PIDs as a count.
type ResolvedLimits struct {
	Memory int64
	CPUs   float64
	PIDs   int
}

// EdgeLimits are the edge container's fixed limits.
var EdgeLimits = ResolvedLimits{Memory: 256 << 20, CPUs: 0.5, PIDs: 64}

// LimitOptions carry the host's harness config. A zero field takes its
// default: ServiceMemory DefaultServiceMemory, RunMemory DefaultRunMemory.
// The caller keeps RunMemory at or below resources.session.memory_high.
type LimitOptions struct {
	ServiceMemory int64
	RunMemory     int64
}

func resolve(l Limits, memory int64) ResolvedLimits {
	r := ResolvedLimits{Memory: memory, CPUs: DefaultCPUs, PIDs: DefaultPIDs}
	// Validate refused an unreadable or infinite size, so an error here
	// cannot happen on a parsed file; the default stands if it does.
	if n, err := cgroup.ParseSize(l.Memory); err == nil && n > 0 {
		r.Memory = n
	}
	if l.CPUs > 0 {
		r.CPUs = l.CPUs
	}
	if l.PIDs > 0 {
		r.PIDs = l.PIDs
	}
	return r
}

// AppLimits are the app container's limits.
func (f *File) AppLimits() ResolvedLimits {
	return resolve(f.App.Limits, DefaultAppMemory)
}

// ServiceLimits are the named service's limits; serviceMemory is the default
// memory (harness.service_memory), 0 for DefaultServiceMemory.
func (f *File) ServiceLimits(name string, serviceMemory int64) ResolvedLimits {
	if serviceMemory <= 0 {
		serviceMemory = DefaultServiceMemory
	}
	return resolve(f.Services[name].Limits, serviceMemory)
}

// MemorySum is the memory of every container a run of the scenario starts:
// the app, the scenario's services and the edge.
func (f *File) MemorySum(scenarioID string, serviceMemory int64) (int64, error) {
	sc, ok := f.Scenario(scenarioID)
	if !ok {
		return 0, &Error{Code: CodeUnknownScenario, Msg: fmt.Sprintf("no scenario %q in the harness file", scenarioID)}
	}
	sum := f.AppLimits().Memory + EdgeLimits.Memory
	for _, name := range sc.Services {
		sum += f.ServiceLimits(name, serviceMemory).Memory
	}
	return sum, nil
}

// CheckLimits refuses a run of the scenario whose memory sum is above the run
// memory cap with CodeTooLarge.
func (f *File) CheckLimits(scenarioID string, opts LimitOptions) error {
	sum, err := f.MemorySum(scenarioID, opts.ServiceMemory)
	if err != nil {
		return err
	}
	limit := opts.RunMemory
	if limit <= 0 {
		limit = DefaultRunMemory
	}
	if sum > limit {
		return &Error{
			Code: CodeTooLarge, Sum: sum, Limit: limit,
			Msg: fmt.Sprintf("scenario %s needs %s of memory, above the run limit %s", scenarioID, cgroup.FormatSize(sum), cgroup.FormatSize(limit)),
		}
	}
	return nil
}

// DefaultAllowedImages is the default harness.allowed_images: the families
// review.Image picks, plus postgres:16.
var DefaultAllowedImages = []string{"postgres:16", "golang:*", "node:*"}

// ImageAllowed reports whether ref matches one of the patterns of
// harness.allowed_images. Both sides are read as name:tag after dropping a
// digest and Docker Hub's docker.io/ and library/ prefixes; a ref with no tag
// has tag latest, and a digest-only ref has an empty tag. A pattern uses
// path.Match syntax, so * matches within one path element: golang:* allows
// every golang tag and never evil/golang. Matching is case sensitive, as image
// references are.
func ImageAllowed(ref string, patterns []string) bool {
	if strings.TrimSpace(ref) == "" {
		return false
	}
	r := normaliseRef(ref)
	for _, p := range patterns {
		if ok, err := path.Match(normaliseRef(p), r); err == nil && ok {
			return true
		}
	}
	return false
}

func normaliseRef(ref string) string {
	ref = strings.TrimSpace(ref)
	name, _, hasDigest := strings.Cut(ref, "@")
	tag := ""
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name, tag = name[:i], name[i+1:]
	} else if !hasDigest {
		tag = "latest"
	}
	name = strings.TrimPrefix(name, "docker.io/")
	name = strings.TrimPrefix(name, "index.docker.io/")
	name = strings.TrimPrefix(name, "library/")
	return name + ":" + tag
}
