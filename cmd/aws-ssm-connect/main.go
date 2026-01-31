package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/e/aws-ssm-connect/internal/config"
	"github.com/e/aws-ssm-connect/internal/output"
	"github.com/e/aws-ssm-connect/internal/ssm"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

var (
	debug   bool
	profile string
	region  string
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "aws-ssm-connect",
	Short: "Connect to AWS EC2 instances via SSM Session Manager",
	Long: `aws-ssm-connect is a CLI tool for connecting to AWS EC2 instances
using AWS Systems Manager Session Manager.

It provides an easy way to start interactive sessions, run commands,
and manage connections to your EC2 instances without requiring SSH keys
or open inbound ports.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("aws-ssm-connect %s\n", version)
		fmt.Printf("  commit: %s\n", commit)
		fmt.Printf("  built:  %s\n", date)
	},
}

var connectCmd = &cobra.Command{
	Use:   "connect [instance-id]",
	Short: "Start an interactive SSM session with an instance",
	Long: `Start an interactive SSM session with the specified EC2 instance.

The instance must have the SSM agent installed and running, and must have
an IAM role that allows SSM connections.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		out := output.New(debug)

		cfg, err := config.Load(profile, region)
		if err != nil {
			return fmt.Errorf("failed to load AWS config: %w", err)
		}

		client := ssm.NewClient(cfg, out)

		var instanceID string
		if len(args) > 0 {
			instanceID = args[0]
		} else {
			// Interactive instance selection
			instanceID, err = client.SelectInstance(ctx)
			if err != nil {
				return err
			}
		}

		return client.StartSession(ctx, instanceID)
	},
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List EC2 instances available for SSM connection",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		out := output.New(debug)

		cfg, err := config.Load(profile, region)
		if err != nil {
			return fmt.Errorf("failed to load AWS config: %w", err)
		}

		client := ssm.NewClient(cfg, out)
		return client.ListInstances(ctx)
	},
}

var execCmd = &cobra.Command{
	Use:   "exec [instance-id] -- [command]",
	Short: "Execute a command on an instance via SSM",
	Long: `Execute a command on the specified EC2 instance using SSM Run Command.

Example:
  aws-ssm-connect exec i-1234567890abcdef0 -- ls -la /tmp`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		out := output.New(debug)

		cfg, err := config.Load(profile, region)
		if err != nil {
			return fmt.Errorf("failed to load AWS config: %w", err)
		}

		instanceID := args[0]
		command := args[1:]

		if len(command) == 0 {
			return fmt.Errorf("no command specified")
		}

		client := ssm.NewClient(cfg, out)
		return client.RunCommand(ctx, instanceID, command)
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "Enable debug output")
	rootCmd.PersistentFlags().StringVarP(&profile, "profile", "p", "", "AWS profile to use")
	rootCmd.PersistentFlags().StringVarP(&region, "region", "r", "", "AWS region to use")

	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(connectCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(execCmd)
}
