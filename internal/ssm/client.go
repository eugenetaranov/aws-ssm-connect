package ssm

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

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
	return cmd.Run()
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
