package images

import (
	"context"

	"github.com/bouwerp/aiman/internal/agentjob"
)

// Review container limits.
const (
	ReviewMemory = agentjob.Memory
	ReviewCPUs   = agentjob.CPUs
	ReviewPIDs   = agentjob.PIDs
	reviewBundle = "/bundle"
)

// ReviewAgents lists the agents the review container can run.
func ReviewAgents() []string { return agentjob.Agents() }

// ReviewContainer is one review run: the agent container on an internal
// network whose only other member is the allowlist proxy.
type ReviewContainer = agentjob.Container

// ReviewRunner runs a review container and returns the agent's stdout.
type ReviewRunner = agentjob.Runner

// DockerReviewRunner runs the review on the host's Docker engine.
type DockerReviewRunner = agentjob.DockerRunner

// DockerRun runs docker with args and stdin and returns stdout and stderr
// apart; err is set only when docker could not run.
type DockerRun = agentjob.DockerRun

// ExecDockerRun runs the docker CLI, keeping at most 1 MiB of stdout.
func ExecDockerRun(ctx context.Context, args []string, stdin string) (string, string, int, error) {
	return agentjob.ExecDockerRun(ctx, args, stdin)
}

// ReviewArgs are the docker run arguments of the agent container.
func ReviewArgs(c ReviewContainer) []string { return agentjob.Args(c) }

// ProxyArgs are the docker run arguments of the allowlist proxy.
func ProxyArgs(c ReviewContainer) []string { return agentjob.ProxyArgs(c) }

// container prepares the run: credential copy, env file and entrypoint.
func (m *ModelReviewer) container(work, image string, rec Record, p pick, info BundleInfo) (ReviewContainer, error) {
	c := ReviewContainer{
		Name: "aiman-review-" + ShortDigest(rec.Digest), Image: image, UID: m.o.UID, GID: m.o.GID,
		AimanBin: m.o.AimanBin, Workdir: reviewBundle,
		Mounts: []agentjob.Mount{{Host: info.Dir, Container: reviewBundle, ReadOnly: true}},
	}
	err := agentjob.Prepare(work, &c, agentjob.Launch{
		Agent: p.entry.Agent, Model: p.entry.Model, Effort: p.entry.Effort, Bin: p.bin, Cred: p.cred,
	})
	return c, err
}
