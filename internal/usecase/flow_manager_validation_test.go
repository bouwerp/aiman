package usecase

import (
	"context"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestCreateSessionRequiresAgent(t *testing.T) {
	manager := &FlowManager{}

	_, err := manager.CreateSession(context.Background(), domain.SessionConfig{})
	if err == nil || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("CreateSession() error = %v, want an agent validation error", err)
	}
}
