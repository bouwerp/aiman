package usecase

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// bestEffortFinaliseTimeout bounds the decorative tail of session creation.
//
// These steps boot agent CLIs on the remote and are slow out of all proportion
// to their value: `claude config get model` alone measured 21.8s against a warm,
// idle box. A session is usable the moment tmux and the agent are running, so
// none of this is worth making the user wait for. Whatever has not finished by
// the deadline is dropped.
const bestEffortFinaliseTimeout = 8 * time.Second

// finaliseSessionBestEffort marks the workspace as trusted and records the
// agent's model. Every step is optional and none can fail the creation.
//
// They run concurrently under a single deadline. Sequentially they measured
// ~31s, all of it after the last progress message, which is what made session
// creation look hung.
func (m *FlowManager) finaliseSessionBestEffort(
	ctx context.Context,
	remote domain.RemoteExecutor,
	session *domain.Session,
	config domain.SessionConfig,
	workingDir string,
) {
	if remote == nil || session == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, bestEffortFinaliseTimeout)
	defer cancel()

	trustCmds := []string{
		fmt.Sprintf("git config --global --add safe.directory %q", workingDir),
		fmt.Sprintf("cd %q && if command -v claude >/dev/null; then claude trust . >/dev/null 2>&1; fi", workingDir),
		fmt.Sprintf("cd %q && if command -v copilot >/dev/null; then copilot trust . >/dev/null 2>&1 || copilot trust add . >/dev/null 2>&1; fi", workingDir),
		fmt.Sprintf("cd %q && if command -v gh >/dev/null; then gh copilot trust . >/dev/null 2>&1 || gh copilot trust add . >/dev/null 2>&1; fi", workingDir),
	}

	var wg sync.WaitGroup
	for _, cmd := range trustCmds {
		wg.Add(1)
		go func(cmd string) {
			defer wg.Done()
			_, _ = remote.Execute(ctx, cmd)
		}(cmd)
	}

	// The model is only a display field, so a miss leaves it empty rather than
	// holding creation open.
	var model string
	if config.Agent != nil {
		wg.Add(1)
		go func(agentName string) {
			defer wg.Done()
			model = detectAgentModel(ctx, remote, agentName)
		}(config.Agent.Name)
	}

	wg.Wait()
	if model != "" {
		session.AgentModel = model
	}
}
