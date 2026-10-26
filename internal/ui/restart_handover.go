package ui

import (
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usecase"
)

// restartHandoverReason is the serve reason for this restart. An agent
// switch is not a resume of the same conversation.
func restartHandoverReason(cfg domain.SessionConfig) string {
	if cfg.FreshAgent {
		return "agent_switch"
	}
	return "restart"
}

// restartPrompt appends the handover instruction only when serve left that
// file. A summary or a missing handoff keeps the prepared prompt. One space
// joins the parts because send-keys submits on newline.
func restartPrompt(prepared string, mode usecase.HandoffMode) string {
	if mode != usecase.HandoffHandover {
		return prepared
	}
	prepared = strings.TrimSpace(prepared)
	if prepared == "" {
		return usecase.HandoverPrompt
	}
	return prepared + " " + usecase.HandoverPrompt
}
