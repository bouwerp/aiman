package usecase

import (
	"context"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// bestEffortFinaliseTimeout bounds the decorative tail of session creation.
//
// Detecting the agent's model boots an agent CLI on the remote and is slow out
// of all proportion to its value: `claude config get model` alone measured
// 21.8s against a warm, idle box. A session is usable the moment the terminal
// and the agent are running, so this is not worth making the user wait for.
// Whatever has not finished by the deadline is dropped.
const bestEffortFinaliseTimeout = 8 * time.Second

// finaliseSessionBestEffort records the agent's model. It is optional and
// cannot fail the creation. Workspace trust runs earlier, before the agent
// process starts — see TrustWorkspacePaths.
func (m *FlowManager) finaliseSessionBestEffort(
	ctx context.Context,
	remote domain.RemoteExecutor,
	session *domain.Session,
	config domain.SessionConfig,
	_ string,
) {
	if remote == nil || session == nil || config.Agent == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, bestEffortFinaliseTimeout)
	defer cancel()

	if model := detectAgentModel(ctx, remote, config.Agent.Name); model != "" {
		session.AgentModel = model
	}
}
