package ec2

import (
	"context"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestNewManager(t *testing.T) {
	mgr := NewManager()
	if mgr == nil {
		t.Fatal("expected non-nil EC2 Manager")
	}
}

func TestWaitUntilSSHReady_Timeout(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Short timeout to verify context/timeout handling
	_, err := mgr.WaitUntilSSHReady(ctx, "default", "us-east-1", "i-nonexistent123", "ubuntu", "", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected error for non-existent instance SSH wait")
	}
}

func TestEC2LaunchSpecDefaults(t *testing.T) {
	spec := domain.EC2LaunchSpec{
		TaskDescription: "Build feature X",
		Repositories:    []string{"https://github.com/owner/repo.git"},
		SelfDestruct:    true,
	}

	if !spec.SelfDestruct {
		t.Errorf("expected SelfDestruct to be true")
	}
	if spec.TaskDescription != "Build feature X" {
		t.Errorf("unexpected task description")
	}
}
