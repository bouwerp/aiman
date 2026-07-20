package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ec2"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/usecase"
)

type envFlagList []string

func (e *envFlagList) String() string {
	return strings.Join(*e, ",")
}

func (e *envFlagList) Set(value string) error {
	*e = append(*e, value)
	return nil
}

func runEC2Loop(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("ec2-loop", flag.ExitOnError)

	var (
		repoList       string
		taskDesc       string
		issueKey       string
		branch         string
		agentName      string
		instanceType   string
		region         string
		awsProfile     string
		subnetID       string
		secGroupID     string
		keyName        string
		noSelfDestruct bool
		timeoutMins    int
		envFlags       envFlagList
	)

	fs.StringVar(&repoList, "repo", "", "Git repository URL(s) to clone (comma-separated for multiple)")
	fs.StringVar(&taskDesc, "task", "", "Description of feature or fix to design, plan, implement, test, and PR")
	fs.StringVar(&issueKey, "issue", "", "JIRA issue key (e.g. PROJ-123)")
	fs.StringVar(&branch, "branch", "", "Target branch name")
	fs.StringVar(&agentName, "agent", "claude", "Autonomous agent name (claude, ageni)")
	fs.StringVar(&instanceType, "instance-type", cfg.EC2Loop.DefaultInstanceType, "AWS EC2 instance type")
	fs.StringVar(&region, "region", cfg.EC2Loop.DefaultRegion, "AWS region")
	fs.StringVar(&awsProfile, "profile", cfg.EC2Loop.DefaultProfile, "AWS profile name")
	fs.StringVar(&subnetID, "subnet", cfg.EC2Loop.DefaultSubnetID, "Subnet ID")
	fs.StringVar(&secGroupID, "security-group", cfg.EC2Loop.DefaultSecurityGroup, "Security Group ID")
	fs.StringVar(&keyName, "key-name", cfg.EC2Loop.DefaultKeyName, "SSH key pair name on AWS")
	fs.BoolVar(&noSelfDestruct, "no-self-destruct", false, "Disable automatic EC2 self-destruction after completion")
	fs.IntVar(&timeoutMins, "timeout", 60, "Timeout in minutes for autonomous loop execution")
	fs.Var(&envFlags, "env", "Environment variable to pass to the agent (e.g., KEY=VALUE). Can be specified multiple times.")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if repoList == "" {
		return fmt.Errorf("--repo is required (e.g. --repo https://github.com/owner/repo.git)")
	}
	if taskDesc == "" {
		return fmt.Errorf("--task is required (e.g. --task 'Implement user authentication refactor')")
	}

	repos := strings.Split(repoList, ",")
	for i := range repos {
		repos[i] = strings.TrimSpace(repos[i])
	}

	envVars := make(map[string]string)
	if ghToken := os.Getenv("GITHUB_TOKEN"); ghToken != "" {
		envVars["GITHUB_TOKEN"] = ghToken
	} else if ghToken := os.Getenv("GH_TOKEN"); ghToken != "" {
		envVars["GH_TOKEN"] = ghToken
	}

	if anthropicKey := os.Getenv("ANTHROPIC_API_KEY"); anthropicKey != "" {
		envVars["ANTHROPIC_API_KEY"] = anthropicKey
	}
	if openaiKey := os.Getenv("OPENAI_API_KEY"); openaiKey != "" {
		envVars["OPENAI_API_KEY"] = openaiKey
	}

	for _, e := range envFlags {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envVars[parts[0]] = parts[1]
		}
	}

	spec := domain.EC2LaunchSpec{
		AWSProfile:      awsProfile,
		Region:          region,
		InstanceType:    instanceType,
		SubnetID:        subnetID,
		SecurityGroupID: secGroupID,
		KeyName:         keyName,
		Repositories:    repos,
		IssueKey:        issueKey,
		Branch:          branch,
		AgentName:       agentName,
		TaskDescription: taskDesc,
		EnvironmentVars: envVars,
		SelfDestruct:    !noSelfDestruct,
		TimeoutMinutes:  timeoutMins,
	}

	ec2Mgr := ec2.NewManager()
	sshFactory := func(host, user, root string) domain.RemoteExecutor {
		return ssh.NewManager(ssh.Config{
			Host: host,
			User: user,
			Root: root,
		})
	}
	runner := usecase.NewEC2LoopRunner(ec2Mgr, sshFactory)

	progressChan := make(chan usecase.EC2LoopProgress, 10)
	go func() {
		for p := range progressChan {
			fmt.Printf("[%s] %s\n", p.Step, p.Message)
		}
	}()

	fmt.Printf("🚀 Starting autonomous loop agent on EC2 (%s / %s)...\n", spec.InstanceType, spec.Region)
	res, err := runner.Run(context.Background(), spec, progressChan)
	close(progressChan)

	if err != nil {
		return fmt.Errorf("autonomous loop failed: %w", err)
	}

	fmt.Println("\n✅ Autonomous EC2 Loop Completed!")
	fmt.Printf("Instance ID: %s\n", res.InstanceID)
	if res.PRURL != "" {
		fmt.Printf("Pull Request: %s\n", res.PRURL)
	}
	fmt.Printf("Duration: %v\n", res.Duration.Round(100*time.Millisecond))
	fmt.Printf("Self Destructed: %v\n", res.SelfDestruct)

	return nil
}
