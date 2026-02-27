package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/hajime-ch/deploy-senpai/internal/cli"
)

var version = "dev"

func main() {
	rootCmd := &cobra.Command{
		Use:   "deploy-senpai",
		Short: "Deploy feature branches to isolated staging environments",
		Long:  "deploy-senpai manages feature branch deployments. Use 'serve' to run the server, or use client commands to interact with a running server.",
		SilenceUsage: true,
	}

	// Persistent flags inherited by all client subcommands
	rootCmd.PersistentFlags().String("server", "", "Server URL (env: DEPLOY_SENPAI_URL, default: http://localhost:8080)")
	rootCmd.PersistentFlags().String("api-key", "", "API key for authentication (env: DEPLOY_SENPAI_API_KEY)")
	rootCmd.PersistentFlags().Bool("json", false, "Output in JSON format")

	// Version subcommand
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version)
		},
	}

	// Register subcommands
	rootCmd.AddCommand(
		newServeCmd(),
		versionCmd,
		cli.NewListCmd(),
		cli.NewDeployCmd(),
		cli.NewStatusCmd(),
		cli.NewRemoveCmd(),
		cli.NewAppsCmd(),
		cli.NewCleanupCmd(),
		cli.NewHealthCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
