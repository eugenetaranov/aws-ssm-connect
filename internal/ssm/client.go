package ssm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/e/aws-ssm-connect/internal/output"
)

// Client provides SSM operations.
type Client struct {
	cfg aws.Config
	ssm *ssm.Client
	ec2 *ec2.Client
	out *output.Output
}

// NewClient creates a new SSM client.
func NewClient(cfg aws.Config, out *output.Output) *Client {
	return &Client{
		cfg: cfg,
		ssm: ssm.NewFromConfig(cfg),
		ec2: ec2.NewFromConfig(cfg),
		out: out,
	}
}

// Instance represents an EC2 instance with SSM status.
type Instance struct {
	ID         string
	Name       string
	State      string
	PrivateIP  string
	SSMStatus  string
	PlatformType string
}

// ListInstances lists all EC2 instances that can be connected via SSM.
func (c *Client) ListInstances(ctx context.Context) error {
	instances, err := c.getSSMInstances(ctx)
	if err != nil {
		return err
	}

	if len(instances) == 0 {
		c.out.Warning("No SSM-managed instances found")
		return nil
	}

	c.out.Header("SSM-Managed Instances")
	fmt.Printf("%-20s %-30s %-10s %-15s %-10s\n", "INSTANCE ID", "NAME", "STATE", "PRIVATE IP", "PLATFORM")
	fmt.Println(strings.Repeat("-", 90))

	for _, inst := range instances {
		fmt.Printf("%-20s %-30s %-10s %-15s %-10s\n",
			inst.ID,
			truncate(inst.Name, 30),
			inst.State,
			inst.PrivateIP,
			inst.PlatformType,
		)
	}

	return nil
}

// SelectInstance prompts the user to select an instance interactively.
func (c *Client) SelectInstance(ctx context.Context) (string, error) {
	instances, err := c.getSSMInstances(ctx)
	if err != nil {
		return "", err
	}

	if len(instances) == 0 {
		return "", fmt.Errorf("no SSM-managed instances found")
	}

	c.out.Header("Select an instance")
	for i, inst := range instances {
		name := inst.Name
		if name == "" {
			name = "(no name)"
		}
		fmt.Printf("  [%d] %s - %s (%s)\n", i+1, inst.ID, name, inst.PrivateIP)
	}

	fmt.Print("\nEnter number: ")
	var selection int
	if _, err := fmt.Scanf("%d", &selection); err != nil {
		return "", fmt.Errorf("invalid selection: %w", err)
	}

	if selection < 1 || selection > len(instances) {
		return "", fmt.Errorf("selection out of range")
	}

	return instances[selection-1].ID, nil
}

// StartSession starts an interactive SSM session with the specified instance.
func (c *Client) StartSession(ctx context.Context, instanceID string) error {
	c.out.Info("Starting session with %s...", instanceID)
	c.out.Debug("Region: %s", c.cfg.Region)

	// Use the AWS CLI session-manager-plugin for interactive sessions
	args := []string{"ssm", "start-session", "--target", instanceID}
	if c.cfg.Region != "" {
		args = append(args, "--region", c.cfg.Region)
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			c.out.Info("Session terminated")
			return nil
		}
		return fmt.Errorf("session failed: %w", err)
	}

	return nil
}

// RunCommand executes a command on the specified instance.
func (c *Client) RunCommand(ctx context.Context, instanceID string, command []string) error {
	cmdStr := strings.Join(command, " ")
	c.out.Info("Executing command on %s: %s", instanceID, cmdStr)

	input := &ssm.SendCommandInput{
		InstanceIds:  []string{instanceID},
		DocumentName: aws.String("AWS-RunShellScript"),
		Parameters: map[string][]string{
			"commands": {cmdStr},
		},
	}

	result, err := c.ssm.SendCommand(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}

	commandID := *result.Command.CommandId
	c.out.Debug("Command ID: %s", commandID)

	// Wait for command completion
	waiter := ssm.NewCommandExecutedWaiter(c.ssm)
	_, err = waiter.WaitForOutput(ctx, &ssm.GetCommandInvocationInput{
		CommandId:  aws.String(commandID),
		InstanceId: aws.String(instanceID),
	}, 0)
	if err != nil {
		return fmt.Errorf("command execution failed: %w", err)
	}

	// Get command output
	output, err := c.ssm.GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{
		CommandId:  aws.String(commandID),
		InstanceId: aws.String(instanceID),
	})
	if err != nil {
		return fmt.Errorf("failed to get command output: %w", err)
	}

	if output.StandardOutputContent != nil && *output.StandardOutputContent != "" {
		fmt.Print(*output.StandardOutputContent)
	}
	if output.StandardErrorContent != nil && *output.StandardErrorContent != "" {
		fmt.Fprint(os.Stderr, *output.StandardErrorContent)
	}

	return nil
}

func (c *Client) getSSMInstances(ctx context.Context) ([]Instance, error) {
	c.out.Debug("Fetching SSM-managed instances...")

	// Get SSM managed instances
	ssmResult, err := c.ssm.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to describe SSM instances: %w", err)
	}

	if len(ssmResult.InstanceInformationList) == 0 {
		return nil, nil
	}

	// Build a set of SSM instance IDs
	ssmInstances := make(map[string]*ssm.DescribeInstanceInformationOutput)
	var instanceIDs []string
	for _, info := range ssmResult.InstanceInformationList {
		if info.InstanceId != nil {
			ssmInstances[*info.InstanceId] = ssmResult
			instanceIDs = append(instanceIDs, *info.InstanceId)
		}
	}

	// Get EC2 instance details
	ec2Result, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: instanceIDs,
	})
	if err != nil {
		c.out.Debug("Failed to get EC2 details: %v", err)
	}

	// Build instance list with EC2 details
	instances := make([]Instance, 0, len(ssmResult.InstanceInformationList))
	ec2Details := make(map[string]*Instance)

	if ec2Result != nil {
		for _, res := range ec2Result.Reservations {
			for _, inst := range res.Instances {
				if inst.InstanceId == nil {
					continue
				}
				name := ""
				for _, tag := range inst.Tags {
					if tag.Key != nil && *tag.Key == "Name" && tag.Value != nil {
						name = *tag.Value
						break
					}
				}
				privateIP := ""
				if inst.PrivateIpAddress != nil {
					privateIP = *inst.PrivateIpAddress
				}
				state := ""
				if inst.State != nil && inst.State.Name != "" {
					state = string(inst.State.Name)
				}
				ec2Details[*inst.InstanceId] = &Instance{
					ID:        *inst.InstanceId,
					Name:      name,
					State:     state,
					PrivateIP: privateIP,
				}
			}
		}
	}

	for _, info := range ssmResult.InstanceInformationList {
		if info.InstanceId == nil {
			continue
		}
		inst := Instance{
			ID:        *info.InstanceId,
			SSMStatus: string(info.PingStatus),
		}
		if info.PlatformType != "" {
			inst.PlatformType = string(info.PlatformType)
		}
		if details, ok := ec2Details[*info.InstanceId]; ok {
			inst.Name = details.Name
			inst.State = details.State
			inst.PrivateIP = details.PrivateIP
		}
		instances = append(instances, inst)
	}

	return instances, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
