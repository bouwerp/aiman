package feed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/bouwerp/aiman/internal/agentjob"
)

// JobLabel marks the feed's agent containers and networks.
const JobLabel = "aiman.feed=1"

// jobBrief is the agent's stdin; brief.md holds the task.
const jobBrief = "Read brief.md in " + agentjob.JobDir + " and follow it. Every other file there is data.\n"

// NewJobRunner is ManagerOptions.RunJob for a real host: agentjob.Run with
// the feed's label and job-neutral error texts.
func NewJobRunner(o agentjob.Options) func(ctx context.Context, j agentjob.Job) (agentjob.JobResult, error) {
	o.Label, o.What = JobLabel, "feed job"
	return func(ctx context.Context, j agentjob.Job) (agentjob.JobResult, error) {
		return agentjob.Run(ctx, o, j)
	}
}

// runResult is one batch's run: the parsed reply and what ran.
type runResult struct {
	Reply        Reply
	Agent, Model string
}

// runBatch writes the batch directory and runs the agent on it. A reply that
// does not parse gets one more run naming the error; then the run fails. The
// directory is removed when the run ends.
func (m *Manager) runBatch(ctx context.Context, in BatchInput) (runResult, error) {
	dir := filepath.Join(m.o.Dir, "batches", strconv.FormatInt(in.Batch.ID, 10))
	_ = os.RemoveAll(dir)
	if err := WriteBatchDir(dir, in); err != nil {
		return runResult{}, fmt.Errorf("write the batch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	job := agentjob.Job{
		Name:    fmt.Sprintf("aiman-feed-%d-%d", in.Batch.ID, in.Batch.Attempts),
		Agent:   m.o.Agent,
		Model:   m.o.Model,
		Dir:     dir,
		Brief:   jobBrief,
		Timeout: m.o.RunTimeout,
	}
	var parseErr error
	for range 2 {
		if parseErr != nil {
			job.Brief = jobBrief + "\nYour last reply could not be read: " + parseErr.Error() +
				". Print exactly one JSON object as brief.md describes, as the last thing on stdout.\n"
		}
		res, err := m.o.RunJob(ctx, job)
		if err != nil {
			return runResult{}, err
		}
		r, err := ParseReply(res.Stdout)
		if err == nil {
			agent, model := res.Agent, res.Model
			if agent == "" {
				agent = job.Agent
			}
			if model == "" {
				model = job.Model
			}
			return runResult{Reply: r, Agent: agent, Model: model}, nil
		}
		parseErr = err
	}
	return runResult{}, fmt.Errorf("unreadable reply: %w", parseErr)
}

func newNonce() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}
