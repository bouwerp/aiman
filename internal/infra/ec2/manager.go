package ec2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

type Manager struct{}

func NewManager() *Manager {
	return &Manager{}
}

type runInstancesOutput struct {
	Instances []struct {
		InstanceId string `json:"InstanceId"`
		State      struct {
			Name string `json:"Name"`
		} `json:"State"`
		PublicIpAddress  string `json:"PublicIpAddress"`
		PublicDnsName    string `json:"PublicDnsName"`
		PrivateIpAddress string `json:"PrivateIpAddress"`
	} `json:"Instances"`
}

type describeInstancesOutput struct {
	Reservations []struct {
		Instances []struct {
			InstanceId string `json:"InstanceId"`
			State      struct {
				Name string `json:"Name"`
			} `json:"State"`
			PublicIpAddress  string `json:"PublicIpAddress"`
			PublicDnsName    string `json:"PublicDnsName"`
			PrivateIpAddress string `json:"PrivateIpAddress"`
			LaunchTime       string `json:"LaunchTime"`
		} `json:"Instances"`
	} `json:"Reservations"`
}

func (m *Manager) LaunchInstance(ctx context.Context, spec domain.EC2LaunchSpec) (*domain.EC2Instance, error) {
	region := spec.Region
	if region == "" {
		region = "us-east-1"
	}
	instanceType := spec.InstanceType
	if instanceType == "" {
		instanceType = "t3.large"
	}
	diskGB := spec.DiskGB
	if diskGB <= 0 {
		diskGB = 30
	}
	tagName := spec.TagName
	if tagName == "" {
		tagName = fmt.Sprintf("aiman-loop-%d", time.Now().Unix())
	}

	amiID := spec.AMIID
	var rootDeviceName string
	if amiID == "" {
		resolvedAMI, resolvedRoot, err := m.resolveLatestUbuntuAMI(ctx, spec.AWSProfile, region)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve AMI: %w", err)
		}
		amiID = resolvedAMI
		rootDeviceName = resolvedRoot
	} else {
		// If AMI is provided, we should look up its root device name if DiskGB > 0
		if spec.DiskGB > 0 {
			resolvedRoot, err := m.getRootDeviceName(ctx, spec.AWSProfile, region, amiID)
			if err == nil {
				rootDeviceName = resolvedRoot
			} else {
				rootDeviceName = "/dev/sda1" // fallback
			}
		}
	}

	args := []string{
		"ec2", "run-instances",
		"--image-id", amiID,
		"--instance-type", instanceType,
		"--region", region,
		"--output", "json",
	}

	if spec.AWSProfile != "" {
		args = append(args, "--profile", spec.AWSProfile)
	}
	if spec.SubnetID != "" {
		args = append(args, "--subnet-id", spec.SubnetID)
	}
	if spec.SecurityGroupID != "" {
		args = append(args, "--security-group-ids", spec.SecurityGroupID)
	}
	if spec.KeyName != "" {
		args = append(args, "--key-name", spec.KeyName)
	}

	if spec.DiskGB > 0 && rootDeviceName != "" {
		blockMapping := fmt.Sprintf(`[{"DeviceName":"%s","Ebs":{"VolumeSize":%d,"VolumeType":"gp3","DeleteOnTermination":true}}]`, rootDeviceName, spec.DiskGB)
		args = append(args, "--block-device-mappings", blockMapping)
	}

	tagSpec := fmt.Sprintf(`ResourceType=instance,Tags=[{Key=Name,Value=%s},{Key=ManagedBy,Value=aiman},{Key=SelfDestruct,Value=true}]`, tagName)
	args = append(args, "--tag-specifications", tagSpec)

	cmd := exec.CommandContext(ctx, "aws", args...)
	var outBytes, errBytes bytes.Buffer
	cmd.Stdout = &outBytes
	cmd.Stderr = &errBytes

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("aws ec2 run-instances failed: %w — stderr: %s", err, errBytes.String())
	}

	var runOut runInstancesOutput
	if err := json.Unmarshal(outBytes.Bytes(), &runOut); err != nil {
		return nil, fmt.Errorf("unmarshal run-instances output: %w", err)
	}
	if len(runOut.Instances) == 0 {
		return nil, fmt.Errorf("run-instances returned no instances")
	}

	inst := runOut.Instances[0]
	return &domain.EC2Instance{
		InstanceID: inst.InstanceId,
		PublicIP:   inst.PublicIpAddress,
		PublicDNS:  inst.PublicDnsName,
		PrivateIP:  inst.PrivateIpAddress,
		State:      inst.State.Name,
		Region:     region,
		LaunchedAt: time.Now(),
	}, nil
}

func (m *Manager) resolveLatestUbuntuAMI(ctx context.Context, profile, region string) (string, string, error) {
	args := []string{
		"ec2", "describe-images",
		"--owners", "099720109477", // Canonical owner ID
		"--filters",
		"Name=name,Values=ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*",
		"Name=state,Values=available",
		"--query", "reverse(sort_by(Images, &CreationDate))[0].[ImageId,RootDeviceName]",
		"--output", "text",
		"--region", region,
	}
	if profile != "" {
		args = append(args, "--profile", profile)
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("describe-images: %w — %s", err, string(out))
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) < 2 {
		return "", "", fmt.Errorf("no Ubuntu AMI found in region %s", region)
	}
	return parts[0], parts[1], nil
}

func (m *Manager) getRootDeviceName(ctx context.Context, profile, region, amiID string) (string, error) {
	args := []string{
		"ec2", "describe-images",
		"--image-ids", amiID,
		"--query", "Images[0].RootDeviceName",
		"--output", "text",
		"--region", region,
	}
	if profile != "" {
		args = append(args, "--profile", profile)
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("describe-images for root device: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (m *Manager) GetInstanceStatus(ctx context.Context, profile, region, instanceID string) (*domain.EC2Instance, error) {
	args := []string{
		"ec2", "describe-instances",
		"--instance-ids", instanceID,
		"--region", region,
		"--output", "json",
	}
	if profile != "" {
		args = append(args, "--profile", profile)
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	var outBytes, errBytes bytes.Buffer
	cmd.Stdout = &outBytes
	cmd.Stderr = &errBytes

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("aws ec2 describe-instances failed: %w — stderr: %s", err, errBytes.String())
	}

	var descOut describeInstancesOutput
	if err := json.Unmarshal(outBytes.Bytes(), &descOut); err != nil {
		return nil, fmt.Errorf("unmarshal describe-instances output: %w", err)
	}
	if len(descOut.Reservations) == 0 || len(descOut.Reservations[0].Instances) == 0 {
		return nil, fmt.Errorf("instance %s not found", instanceID)
	}

	inst := descOut.Reservations[0].Instances[0]
	t, _ := time.Parse(time.RFC3339, inst.LaunchTime)

	return &domain.EC2Instance{
		InstanceID: inst.InstanceId,
		PublicIP:   inst.PublicIpAddress,
		PublicDNS:  inst.PublicDnsName,
		PrivateIP:  inst.PrivateIpAddress,
		State:      inst.State.Name,
		Region:     region,
		LaunchedAt: t,
	}, nil
}

func (m *Manager) WaitUntilSSHReady(ctx context.Context, profile, region, instanceID, user, keyPath string, timeout time.Duration) (*domain.EC2Instance, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		inst, err := m.GetInstanceStatus(ctx, profile, region, instanceID)
		if err == nil && inst.State == "running" && inst.PublicIP != "" {
			// Check TCP connection to SSH port 22
			conn, netErr := net.DialTimeout("tcp", net.JoinHostPort(inst.PublicIP, "22"), 3*time.Second)
			if netErr == nil {
				conn.Close()
				return inst, nil
			}
		}

		time.Sleep(5 * time.Second)
	}

	return nil, fmt.Errorf("timed out after %v waiting for instance %s to be SSH ready", timeout, instanceID)
}

func (m *Manager) TerminateInstance(ctx context.Context, profile, region, instanceID string) error {
	args := []string{
		"ec2", "terminate-instances",
		"--instance-ids", instanceID,
		"--region", region,
		"--output", "json",
	}
	if profile != "" {
		args = append(args, "--profile", profile)
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	var errBytes bytes.Buffer
	cmd.Stderr = &errBytes

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("aws ec2 terminate-instances failed: %w — stderr: %s", err, errBytes.String())
	}
	return nil
}
