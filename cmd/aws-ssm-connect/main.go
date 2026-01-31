package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/e/aws-ssm-connect/internal/config"
	"github.com/e/aws-ssm-connect/internal/output"
	"github.com/e/aws-ssm-connect/internal/selector"
	"github.com/e/aws-ssm-connect/internal/ssm"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

var (
	debug       bool
	profile     string
	region      string
	showVersion bool
	listFlag    bool
	copyFlag    bool
	runFlag     bool
)

func main() {
	// Allow single-dash long flags
	for i, arg := range os.Args {
		switch arg {
		case "-copy":
			os.Args[i] = "--copy"
		case "-run":
			os.Args[i] = "--run"
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "aws-ssm-connect [name]",
	Short: "Connect to AWS EC2 instances via SSM Session Manager",
	Long: `aws-ssm-connect is a CLI tool for connecting to AWS EC2 instances
using AWS Systems Manager Session Manager.

Run without arguments for interactive fuzzy selection, or provide
an instance name/ID to filter and connect directly.

Use -l to list instances: -l [filter words...]
Use -copy to copy files: -copy src dst (use instance:/path for remote)
Use -run to run a command: -run instance "command"`,
	Args:          cobra.ArbitraryArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if showVersion {
			fmt.Printf("aws-ssm-connect %s\n", version)
			fmt.Printf("  commit: %s\n", commit)
			fmt.Printf("  built:  %s\n", date)
			return nil
		}

		ctx := cmd.Context()
		out := output.New(debug)

		cfg, err := config.Load(profile, region)
		if err != nil {
			return fmt.Errorf("failed to load AWS config: %w", err)
		}

		client := ssm.NewClient(cfg, out)

		// Handle -c flag for file upload
		if copyFlag {
			return handleCopy(ctx, client, args)
		}

		// Handle -l flag for listing instances
		if listFlag {
			return handleList(ctx, client, args)
		}

		// Handle -run flag for running a command
		if runFlag {
			return handleRun(ctx, client, args)
		}

		var instanceID, instanceName string
		if len(args) > 1 {
			return fmt.Errorf("too many arguments; use -l for listing with filters")
		}
		if len(args) > 0 {
			// Name/ID provided - filter and select
			instanceID, instanceName, err = client.SelectByName(ctx, args[0])
			if err != nil {
				return err
			}
		} else {
			// No args - interactive fuzzy selection
			instanceID, instanceName, err = client.SelectInstance(ctx)
			if err != nil {
				return err
			}
		}

		return client.StartSession(ctx, instanceID, instanceName, profile)
	},
}

// handleList handles the -l flag for listing instances.
func handleList(ctx context.Context, client *ssm.Client, filters []string) error {
	instances, err := client.GetRunningInstances(ctx)
	if err != nil {
		return err
	}

	if len(instances) == 0 {
		fmt.Println("No running SSM-managed instances found")
		return nil
	}

	// Filter instances if filter words provided
	if len(filters) > 0 {
		var filtered []selector.Instance
		for _, inst := range instances {
			if matchesAllFilters(inst, filters) {
				filtered = append(filtered, inst)
			}
		}
		instances = filtered
	}

	if len(instances) == 0 {
		fmt.Println("No instances match the filters")
		return nil
	}

	for _, inst := range instances {
		if inst.Name != "" {
			fmt.Printf("%s\t%s\t%s\n", inst.ID, inst.Name, inst.PrivateIP)
		} else {
			fmt.Printf("%s\t%s\n", inst.ID, inst.PrivateIP)
		}
	}
	return nil
}

// matchesAllFilters checks if instance matches all filter words (case-insensitive).
func matchesAllFilters(inst selector.Instance, filters []string) bool {
	searchText := strings.ToLower(inst.ID + " " + inst.Name + " " + inst.PrivateIP)
	for _, f := range filters {
		if !strings.Contains(searchText, strings.ToLower(f)) {
			return false
		}
	}
	return true
}

// handleRun handles the -run flag for running a command on an instance.
// Format: -run instance "command"
func handleRun(ctx context.Context, client *ssm.Client, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: aws-ssm-connect -run <instance> <command>")
	}

	instance := args[0]
	command := strings.Join(args[1:], " ")

	// Resolve instance ID if name was provided
	instanceID, err := resolveInstance(ctx, client, instance)
	if err != nil {
		return err
	}

	return client.RunCommand(ctx, instanceID, command)
}

// handleCopy handles the -copy flag for file copy (upload or download).
// Format: -copy src dst (use instance:/path for remote)
func handleCopy(ctx context.Context, client *ssm.Client, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: aws-ssm-connect -copy <src> <dst> (use instance:/path for remote)")
	}

	src, dst := args[0], args[1]

	// Detect direction based on which arg has instance: format
	srcInstance, srcPath := parseRemotePath(src)
	dstInstance, dstPath := parseRemotePath(dst)

	if srcInstance != "" && dstInstance != "" {
		return fmt.Errorf("cannot copy between two remote instances")
	}
	if srcInstance == "" && dstInstance == "" {
		return fmt.Errorf("one of src or dst must be remote (instance:/path)")
	}

	var instanceID string
	var err error

	if dstInstance != "" {
		// Upload: local -> remote
		if instanceID, err = resolveInstance(ctx, client, dstInstance); err != nil {
			return err
		}
		return client.UploadFile(ctx, src, instanceID, dstPath)
	}

	// Download: remote -> local
	if instanceID, err = resolveInstance(ctx, client, srcInstance); err != nil {
		return err
	}
	return client.DownloadFile(ctx, instanceID, srcPath, dst)
}

// parseRemotePath parses "instance:/path" format, returns ("", path) if local.
func parseRemotePath(s string) (instance, path string) {
	idx := strings.Index(s, ":")
	if idx == -1 {
		return "", s
	}
	// Check if it looks like instance:path (not just /abs/path on windows or similar)
	instance = s[:idx]
	path = s[idx+1:]
	if instance == "" || path == "" {
		return "", s
	}
	return instance, path
}

// resolveInstance resolves instance name to ID.
func resolveInstance(ctx context.Context, client *ssm.Client, instance string) (string, error) {
	if strings.HasPrefix(instance, "i-") {
		return instance, nil
	}
	id, _, err := client.SelectByName(ctx, instance)
	return id, err
}

func init() {
	rootCmd.Flags().BoolVarP(&showVersion, "version", "v", false, "Show version information")
	rootCmd.Flags().BoolVarP(&debug, "debug", "d", false, "Enable debug output")
	rootCmd.Flags().StringVar(&profile, "profile", "", "AWS profile to use")
	rootCmd.Flags().StringVar(&region, "region", "", "AWS region to use")
	rootCmd.Flags().BoolVar(&copyFlag, "copy", false, "Copy file to instance")
	rootCmd.Flags().BoolVarP(&listFlag, "list", "l", false, "List instances and exit")
	rootCmd.Flags().BoolVar(&runFlag, "run", false, "Run a command on instance")
}
