package domain

import (
	"context"
	"time"
)

// EC2LaunchSpec specifies parameters for launching an on-demand AWS EC2 instance.
type EC2LaunchSpec struct {
	AWSProfile      string            `json:"aws_profile,omitempty"`
	Region          string            `json:"region,omitempty"`
	InstanceType    string            `json:"instance_type,omitempty"`
	AMIID           string            `json:"ami_id,omitempty"`
	SubnetID        string            `json:"subnet_id,omitempty"`
	SecurityGroupID string            `json:"security_group_id,omitempty"`
	KeyName         string            `json:"key_name,omitempty"`
	SSHKeyPath      string            `json:"ssh_key_path,omitempty"`
	SSHUser         string            `json:"ssh_user,omitempty"`
	DiskGB          int               `json:"disk_gb,omitempty"`
	TagName         string            `json:"tag_name,omitempty"`
	Repositories    []string          `json:"repositories"` // List of git URLs to clone
	IssueKey        string            `json:"issue_key,omitempty"`
	Branch          string            `json:"branch,omitempty"`
	AgentName       string            `json:"agent_name,omitempty"`
	TaskDescription string            `json:"task_description"`
	EnvironmentVars map[string]string `json:"environment_vars,omitempty"`
	SelfDestruct    bool              `json:"self_destruct"`
	TimeoutMinutes  int               `json:"timeout_minutes,omitempty"`
}

// EC2Instance holds metadata about a launched EC2 instance.
type EC2Instance struct {
	InstanceID string    `json:"instance_id"`
	PublicIP   string    `json:"public_ip"`
	PublicDNS  string    `json:"public_dns"`
	PrivateIP  string    `json:"private_ip"`
	State      string    `json:"state"`
	Region     string    `json:"region"`
	LaunchedAt time.Time `json:"launched_at"`
}

// EC2LoopResult holds the outcome of an autonomous loop run on EC2.
type EC2LoopResult struct {
	InstanceID   string        `json:"instance_id"`
	PRURL        string        `json:"pr_url,omitempty"`
	Success      bool          `json:"success"`
	Error        string        `json:"error,omitempty"`
	Duration     time.Duration `json:"duration"`
	SelfDestruct bool          `json:"self_destructed"`
}

// EC2Manager manages EC2 instance lifecycles.
type EC2Manager interface {
	LaunchInstance(ctx context.Context, spec EC2LaunchSpec) (*EC2Instance, error)
	GetInstanceStatus(ctx context.Context, profile, region, instanceID string) (*EC2Instance, error)
	WaitUntilSSHReady(ctx context.Context, profile, region, instanceID, user, keyPath string, timeout time.Duration) (*EC2Instance, error)
	TerminateInstance(ctx context.Context, profile, region, instanceID string) error
}
