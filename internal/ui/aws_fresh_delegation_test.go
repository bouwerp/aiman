package ui

import (
	"errors"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func swapDiskConfig(t *testing.T, cfg *config.Config, err error) {
	t.Helper()
	old := loadDiskConfig
	loadDiskConfig = func() (*config.Config, error) { return cfg, err }
	t.Cleanup(func() { loadDiskConfig = old })
}

func TestFreshDelegationTakesTheFileRegions(t *testing.T) {
	mem := &config.AWSDelegation{Profile: "lab", Regions: []string{"us-east-2"}}
	disk := &config.AWSDelegation{Profile: "lab", Regions: []string{"us-east-1", "us-east-2", "af-south-1"}}
	swapDiskConfig(t, &config.Config{Remotes: []config.Remote{
		{Host: "10.0.1.5", User: "code", AWSDelegations: []*config.AWSDelegation{{Profile: "lab", Regions: []string{"eu-west-1"}}}},
		{Host: "10.0.1.9", User: "code", AWSDelegations: []*config.AWSDelegation{{Profile: "dev"}, disk}},
	}}, nil)

	got := freshDelegation(config.Remote{Host: "10.0.1.9", User: "code"}, mem)
	if got != disk {
		t.Fatalf("got %+v, want the file's entry on the same host", got)
	}
}

func TestFreshDelegationKeepsMemoryWithoutAMatch(t *testing.T) {
	mem := &config.AWSDelegation{Profile: "lab", Regions: []string{"us-east-2"}}
	remote := config.Remote{Host: "10.0.1.9", User: "code"}

	swapDiskConfig(t, nil, errors.New("unreadable"))
	if got := freshDelegation(remote, mem); got != mem {
		t.Fatalf("read error: got %+v, want the in-memory entry", got)
	}

	swapDiskConfig(t, &config.Config{Remotes: []config.Remote{
		{Host: "10.0.1.9", User: "other", AWSDelegation: &config.AWSDelegation{Profile: "lab"}},
		{Host: "10.0.1.9", User: "code", AWSDelegation: &config.AWSDelegation{Profile: "prod"}},
	}}, nil)
	if got := freshDelegation(remote, mem); got != mem {
		t.Fatalf("no match: got %+v, want the in-memory entry", got)
	}
	if got := freshDelegation(remote, nil); got != nil {
		t.Fatalf("nil delegation: got %+v", got)
	}
}
