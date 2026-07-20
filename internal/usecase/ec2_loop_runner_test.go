package usecase

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

type mockEC2Manager struct {
	launchedInst *domain.EC2Instance
	launchErr    error
	sshReadyErr  error
	termErr      error
	terminatedID string
}

func (m *mockEC2Manager) LaunchInstance(ctx context.Context, spec domain.EC2LaunchSpec) (*domain.EC2Instance, error) {
	if m.launchErr != nil {
		return nil, m.launchErr
	}
	if m.launchedInst != nil {
		return m.launchedInst, nil
	}
	return &domain.EC2Instance{
		InstanceID: "i-mock12345",
		PublicIP:   "127.0.0.1",
		PublicDNS:  "localhost",
		State:      "running",
		Region:     spec.Region,
		LaunchedAt: time.Now(),
	}, nil
}

func (m *mockEC2Manager) GetInstanceStatus(ctx context.Context, profile, region, instanceID string) (*domain.EC2Instance, error) {
	return &domain.EC2Instance{
		InstanceID: instanceID,
		PublicIP:   "127.0.0.1",
		State:      "running",
		Region:     region,
	}, nil
}

func (m *mockEC2Manager) WaitUntilSSHReady(ctx context.Context, profile, region, instanceID, user, keyPath string, timeout time.Duration) (*domain.EC2Instance, error) {
	if m.sshReadyErr != nil {
		return nil, m.sshReadyErr
	}
	return m.GetInstanceStatus(ctx, profile, region, instanceID)
}

func (m *mockEC2Manager) TerminateInstance(ctx context.Context, profile, region, instanceID string) error {
	m.terminatedID = instanceID
	return m.termErr
}

func TestExtractRepoName(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{"https://github.com/bouwerp/aiman.git", "aiman"},
		{"git@github.com:bouwerp/aiman.git", "aiman"},
		{"https://github.com/org/my-repo", "my-repo"},
	}

	for _, tt := range tests {
		got := extractRepoName(tt.url)
		if got != tt.expected {
			t.Errorf("extractRepoName(%q) = %q, expected %q", tt.url, got, tt.expected)
		}
	}
}

func TestEC2LoopRunner_LaunchErrorSelfDestruct(t *testing.T) {
	mockMgr := &mockEC2Manager{
		launchErr: fmt.Errorf("quota exceeded"),
	}
	sshFactory := func(host, user, root string) domain.RemoteExecutor {
		return nil
	}
	runner := NewEC2LoopRunner(mockMgr, sshFactory)

	spec := domain.EC2LaunchSpec{
		Region:          "us-east-1",
		TaskDescription: "Add test feature",
		Repositories:    []string{"https://github.com/owner/repo.git"},
		SelfDestruct:    true,
	}

	res, err := runner.Run(context.Background(), spec, nil)
	if err == nil {
		t.Fatal("expected launch error")
	}
	if res != nil {
		t.Fatalf("expected nil result on launch failure, got %+v", res)
	}
}
