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
	debug       bool
	profile     string
	region      string
	showVersion bool
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
	Use:   "aws-ssm-connect [name]",
	Short: "Connect to AWS EC2 instances via SSM Session Manager",
	Long: `aws-ssm-connect is a CLI tool for connecting to AWS EC2 instances
using AWS Systems Manager Session Manager.

Run without arguments for interactive fuzzy selection, or provide
an instance name/ID to filter and connect directly.`,
	Args:          cobra.MaximumNArgs(1),
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

		var instanceID, instanceName string
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

func init() {
	rootCmd.Flags().BoolVarP(&showVersion, "version", "v", false, "Show version information")
	rootCmd.Flags().BoolVarP(&debug, "debug", "d", false, "Enable debug output")
	rootCmd.Flags().StringVar(&profile, "profile", "", "AWS profile to use")
	rootCmd.Flags().StringVar(&region, "region", "", "AWS region to use")
}
