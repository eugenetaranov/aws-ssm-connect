package ssm

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/e/aws-ssm-connect/internal/history"
	"github.com/e/aws-ssm-connect/internal/output"
	"github.com/e/aws-ssm-connect/internal/selector"
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
	ID           string
	Name         string
	State        string
	PrivateIP    string
	SSMStatus    string
	PlatformType string
}

// GetRunningInstances returns running instances that can be connected via SSM.
func (c *Client) GetRunningInstances(ctx context.Context) ([]selector.Instance, error) {
	instances, err := c.getSSMInstances(ctx)
	if err != nil {
		return nil, err
	}

	var running []selector.Instance
	for _, inst := range instances {
		if inst.State == "running" {
			running = append(running, selector.Instance{
				ID:        inst.ID,
				Name:      inst.Name,
				PrivateIP: inst.PrivateIP,
			})
		}
	}

	return running, nil
}

// SelectInstance prompts the user to select an instance using fuzzy finder.
// Returns instance ID and name.
func (c *Client) SelectInstance(ctx context.Context) (string, string, error) {
	instances, err := c.GetRunningInstances(ctx)
	if err != nil {
		return "", "", err
	}

	if len(instances) == 0 {
		return "", "", fmt.Errorf("no running SSM-managed instances found")
	}

	// Load history to show recent instances first
	hist, _ := history.Load()

	selected, err := selector.SelectInstance(instances, hist.RecentIDs()...)
	if err != nil {
		return "", "", err
	}

	return selected.ID, selected.Name, nil
}

// SelectByName finds instances by name and returns the matching instance ID and name.
// If multiple instances match, presents fuzzy finder for selection.
func (c *Client) SelectByName(ctx context.Context, name string) (string, string, error) {
	instances, err := c.GetRunningInstances(ctx)
	if err != nil {
		return "", "", err
	}

	if len(instances) == 0 {
		return "", "", fmt.Errorf("no running SSM-managed instances found")
	}

	matches := selector.FindByName(instances, name)
	if len(matches) == 0 {
		return "", "", fmt.Errorf("no instances found matching %q", name)
	}

	if len(matches) == 1 {
		return matches[0].ID, matches[0].Name, nil
	}

	// Multiple matches - let user select
	hist, _ := history.Load()
	selected, err := selector.SelectInstance(matches, hist.RecentIDs()...)
	if err != nil {
		return "", "", err
	}

	return selected.ID, selected.Name, nil
}

// StartSession starts an interactive SSM session with the specified instance.
func (c *Client) StartSession(ctx context.Context, instanceID, instanceName, profile string) error {
	c.out.Info("Starting session with %s...", instanceID)
	c.out.Debug("Region: %s", c.cfg.Region)

	// Save to history (unless disabled)
	if os.Getenv("AWS_SSM_CONNECT_HISTORY_DISABLED") == "" {
		if hist, err := history.Load(); err == nil {
			_ = hist.Add(instanceID, instanceName)
		}
	}

	// Call StartSession API using SDK
	input := &ssm.StartSessionInput{
		Target: &instanceID,
	}
	resp, err := c.ssm.StartSession(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to start session: %w", err)
	}

	// Find session-manager-plugin
	pluginPath, err := exec.LookPath("session-manager-plugin")
	if err != nil {
		return fmt.Errorf("session-manager-plugin not found (install via: brew install session-manager-plugin): %w", err)
	}

	// Build session response JSON for the plugin
	sessionJSON := fmt.Sprintf(`{"SessionId":"%s","StreamUrl":"%s","TokenValue":"%s"}`,
		*resp.SessionId, *resp.StreamUrl, *resp.TokenValue)

	// Build target JSON
	targetJSON := fmt.Sprintf(`{"Target":"%s"}`, instanceID)

	// session-manager-plugin <session-json> <region> StartSession <profile> <target-json>
	args := []string{
		sessionJSON,
		c.cfg.Region,
		"StartSession",
		profile,
		targetJSON,
	}

	// Open fresh /dev/tty for the plugin
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("failed to open /dev/tty: %w", err)
	}
	defer tty.Close()

	cmd := exec.Command(pluginPath, args...)
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	err = cmd.Run()

	// Print instance info on exit
	if instanceName != "" {
		fmt.Printf("Disconnected from %s %s\n", instanceName, instanceID)
	} else {
		fmt.Printf("Disconnected from %s\n", instanceID)
	}

	return err
}

const maxUploadSize = 100 * 1024 // 100KB limit due to SSM command size constraints

// UploadFile uploads a local file to a remote instance via SSM SendCommand.
func (c *Client) UploadFile(ctx context.Context, localPath, instanceID, remotePath string) error {
	// Read and validate local file
	data, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("failed to read local file: %w", err)
	}

	if len(data) > maxUploadSize {
		return fmt.Errorf("file size %d bytes exceeds maximum allowed size of %d bytes (100KB)", len(data), maxUploadSize)
	}

	c.out.Info("Uploading %s (%d bytes) to %s:%s", localPath, len(data), instanceID, remotePath)

	// Base64 encode the file content
	encoded := base64.StdEncoding.EncodeToString(data)

	// Build shell script to decode and write file
	script := fmt.Sprintf("echo '%s' | base64 -d > '%s'", encoded, remotePath)

	// Send command
	c.out.Debug("Sending command to instance...")
	sendResult, err := c.ssm.SendCommand(ctx, &ssm.SendCommandInput{
		InstanceIds:  []string{instanceID},
		DocumentName: aws.String("AWS-RunShellScript"),
		Parameters: map[string][]string{
			"commands": {script},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}

	commandID := *sendResult.Command.CommandId
	c.out.Debug("Command ID: %s", commandID)

	// Poll for completion
	if err := c.waitForCommand(ctx, commandID, instanceID); err != nil {
		return err
	}

	c.out.Info("Upload complete")
	return nil
}

// DownloadFile downloads a remote file from an instance via SSM SendCommand.
func (c *Client) DownloadFile(ctx context.Context, instanceID, remotePath, localPath string) error {
	c.out.Info("Downloading %s:%s to %s", instanceID, remotePath, localPath)

	// Read and base64 encode the remote file
	script := fmt.Sprintf("base64 '%s'", remotePath)

	c.out.Debug("Sending command to instance...")
	sendResult, err := c.ssm.SendCommand(ctx, &ssm.SendCommandInput{
		InstanceIds:  []string{instanceID},
		DocumentName: aws.String("AWS-RunShellScript"),
		Parameters: map[string][]string{
			"commands": {script},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}

	commandID := *sendResult.Command.CommandId
	c.out.Debug("Command ID: %s", commandID)

	// Poll for completion and get output
	output, err := c.waitForCommandOutput(ctx, commandID, instanceID)
	if err != nil {
		return err
	}

	// Decode base64 output
	data, err := base64.StdEncoding.DecodeString(output)
	if err != nil {
		return fmt.Errorf("failed to decode file content: %w", err)
	}

	// Write to local file
	if err := os.WriteFile(localPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write local file: %w", err)
	}

	c.out.Info("Download complete (%d bytes)", len(data))
	return nil
}

func (c *Client) waitForCommand(ctx context.Context, commandID, instanceID string) error {
	_, err := c.waitForCommandOutput(ctx, commandID, instanceID)
	return err
}

func (c *Client) waitForCommandOutput(ctx context.Context, commandID, instanceID string) (string, error) {
	pollInterval := 500 * time.Millisecond
	maxInterval := 5 * time.Second

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}

		result, err := c.ssm.GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{
			CommandId:  aws.String(commandID),
			InstanceId: aws.String(instanceID),
		})
		if err != nil {
			// InvocationDoesNotExist means command hasn't registered yet
			c.out.Debug("Waiting for command to register...")
			pollInterval = min(pollInterval*2, maxInterval)
			continue
		}

		switch result.Status {
		case ssmtypes.CommandInvocationStatusSuccess:
			output := ""
			if result.StandardOutputContent != nil {
				output = *result.StandardOutputContent
			}
			return output, nil
		case ssmtypes.CommandInvocationStatusFailed,
			ssmtypes.CommandInvocationStatusTimedOut,
			ssmtypes.CommandInvocationStatusCancelled:
			errMsg := ""
			if result.StandardErrorContent != nil && *result.StandardErrorContent != "" {
				errMsg = *result.StandardErrorContent
			}
			return "", fmt.Errorf("command %s: %s", result.Status, errMsg)
		case ssmtypes.CommandInvocationStatusInProgress,
			ssmtypes.CommandInvocationStatusPending:
			c.out.Debug("Command status: %s", result.Status)
			pollInterval = min(pollInterval*2, maxInterval)
		default:
			c.out.Debug("Unknown status: %s", result.Status)
			pollInterval = min(pollInterval*2, maxInterval)
		}
	}
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

	// Collect SSM instance IDs
	var instanceIDs []string
	for _, info := range ssmResult.InstanceInformationList {
		if info.InstanceId != nil {
			instanceIDs = append(instanceIDs, *info.InstanceId)
		}
	}

	// Get EC2 instance details (only running instances)
	ec2Result, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: instanceIDs,
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("instance-state-name"),
				Values: []string{"running"},
			},
		},
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
