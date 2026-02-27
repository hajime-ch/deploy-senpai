package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

// DTO structs — mirrors server JSON responses, avoids importing internal/deployer.

type Deployment struct {
	ID            string `json:"id"`
	App           string `json:"app"`
	Branch        string `json:"branch"`
	SanitizedName string `json:"sanitized_name"`
	URL           string `json:"url"`
	ImageTag      string `json:"image_tag"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	LastActivity  string `json:"last_activity"`
	Status        string `json:"status"`
	ErrorMessage  string `json:"error_message,omitempty"`
}

type AppInfo struct {
	Name             string `json:"name"`
	Repo             string `json:"repo"`
	ComposeTemplate  string `json:"compose_template"`
}

type HealthResponse struct {
	Status     string                       `json:"status"`
	Time       string                       `json:"time"`
	Components map[string]HealthComponent   `json:"components"`
}

type HealthComponent struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	Count   int    `json:"count,omitempty"`
	Error   string `json:"error,omitempty"`
}

// NewListCmd creates the `list` subcommand.
func NewListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all deployments",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := NewClientFromCmd(cmd)
			data, status, err := client.Get(cmd.Context(), "/api/v1/deployments")
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return fmt.Errorf("server returned %d: %s", status, string(data))
			}

			var deployments []Deployment
			if err := json.Unmarshal(data, &deployments); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			if IsJSON(cmd) {
				return PrintJSON(deployments)
			}

			if len(deployments) == 0 {
				fmt.Println("No deployments found.")
				return nil
			}

			t := NewTable()
			t.Header("STATUS", "APP", "BRANCH", "URL", "IMAGE TAG", "UPDATED")
			for _, d := range deployments {
				updated := parseAndFormat(d.UpdatedAt)
				t.Row(
					StatusIcon(d.Status)+" "+d.Status,
					d.App,
					d.Branch,
					d.URL,
					d.ImageTag,
					updated,
				)
			}
			t.Flush()
			return nil
		},
	}
	return cmd
}

// NewDeployCmd creates the `deploy` subcommand.
func NewDeployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy <app> <branch>",
		Short: "Deploy an app branch",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, branch := args[0], args[1]
			imageTag, _ := cmd.Flags().GetString("image-tag")
			wait, _ := cmd.Flags().GetBool("wait")
			timeout, _ := cmd.Flags().GetDuration("timeout")

			client := NewClientFromCmd(cmd)

			// Build request body
			body := map[string]string{}
			if imageTag != "" {
				body["image_tag"] = imageTag
			}
			bodyJSON, _ := json.Marshal(body)

			path := fmt.Sprintf("/api/v1/deployments/%s/%s", app, branch)
			data, status, err := client.Post(cmd.Context(), path, bytes.NewReader(bodyJSON))
			if err != nil {
				return err
			}
			if status != http.StatusCreated && status != http.StatusOK {
				return fmt.Errorf("server returned %d: %s", status, string(data))
			}

			var dep Deployment
			if err := json.Unmarshal(data, &dep); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			if !wait {
				if IsJSON(cmd) {
					return PrintJSON(dep)
				}
				fmt.Printf("Deployment triggered: %s/%s (id: %s)\n", dep.App, dep.Branch, dep.ID)
				fmt.Printf("URL: %s\n", dep.URL)
				return nil
			}

			// Poll until running or failed
			fmt.Printf("Waiting for deployment %s/%s...\n", app, branch)
			deadline := time.After(timeout)
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-deadline:
					return fmt.Errorf("timed out waiting for deployment (last status: %s)", dep.Status)
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-ticker.C:
					data, status, err = client.Get(cmd.Context(), path)
					if err != nil {
						return err
					}
					if status != http.StatusOK {
						return fmt.Errorf("server returned %d: %s", status, string(data))
					}
					if err := json.Unmarshal(data, &dep); err != nil {
						return fmt.Errorf("parsing response: %w", err)
					}

					switch dep.Status {
					case "running":
						if IsJSON(cmd) {
							return PrintJSON(dep)
						}
						fmt.Printf("Deployment is running!\n")
						fmt.Printf("URL: %s\n", dep.URL)
						return nil
					case "failed":
						if IsJSON(cmd) {
							return PrintJSON(dep)
						}
						return fmt.Errorf("deployment failed: %s", dep.ErrorMessage)
					default:
						fmt.Printf("  status: %s\n", dep.Status)
					}
				}
			}
		},
	}
	cmd.Flags().String("image-tag", "", "Docker image tag to deploy")
	cmd.Flags().Bool("wait", false, "Wait for deployment to reach running or failed state")
	cmd.Flags().Duration("timeout", 5*time.Minute, "Timeout when using --wait")
	return cmd
}

// NewStatusCmd creates the `status` subcommand.
func NewStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <app> <branch>",
		Short: "Show deployment status",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, branch := args[0], args[1]
			client := NewClientFromCmd(cmd)

			path := fmt.Sprintf("/api/v1/deployments/%s/%s", app, branch)
			data, status, err := client.Get(cmd.Context(), path)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return fmt.Errorf("server returned %d: %s", status, string(data))
			}

			var dep Deployment
			if err := json.Unmarshal(data, &dep); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			if IsJSON(cmd) {
				return PrintJSON(dep)
			}

			fmt.Printf("App:        %s\n", dep.App)
			fmt.Printf("Branch:     %s\n", dep.Branch)
			fmt.Printf("Status:     %s %s\n", StatusIcon(dep.Status), dep.Status)
			fmt.Printf("URL:        %s\n", dep.URL)
			fmt.Printf("Image Tag:  %s\n", dep.ImageTag)
			fmt.Printf("ID:         %s\n", dep.ID)
			fmt.Printf("Created:    %s\n", parseAndFormat(dep.CreatedAt))
			fmt.Printf("Updated:    %s\n", parseAndFormat(dep.UpdatedAt))
			if dep.ErrorMessage != "" {
				fmt.Printf("Error:      %s\n", dep.ErrorMessage)
			}
			return nil
		},
	}
	return cmd
}

// NewRemoveCmd creates the `remove` subcommand.
func NewRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "remove <app> <branch>",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove a deployment",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, branch := args[0], args[1]
			client := NewClientFromCmd(cmd)

			path := fmt.Sprintf("/api/v1/deployments/%s/%s", app, branch)
			data, status, err := client.Delete(cmd.Context(), path)
			if err != nil {
				return err
			}
			if status != http.StatusNoContent && status != http.StatusOK {
				return fmt.Errorf("server returned %d: %s", status, string(data))
			}

			if IsJSON(cmd) {
				return PrintJSON(map[string]string{"message": "deployment removed"})
			}

			fmt.Printf("Deployment %s/%s removed.\n", app, branch)
			return nil
		},
	}
	return cmd
}

// NewAppsCmd creates the `apps` subcommand.
func NewAppsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apps",
		Short: "List configured apps",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := NewClientFromCmd(cmd)
			data, status, err := client.Get(cmd.Context(), "/api/v1/apps")
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return fmt.Errorf("server returned %d: %s", status, string(data))
			}

			var apps []AppInfo
			if err := json.Unmarshal(data, &apps); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			if IsJSON(cmd) {
				return PrintJSON(apps)
			}

			if len(apps) == 0 {
				fmt.Println("No apps configured.")
				return nil
			}

			t := NewTable()
			t.Header("NAME", "REPO", "TEMPLATE")
			for _, a := range apps {
				t.Row(a.Name, a.Repo, a.ComposeTemplate)
			}
			t.Flush()
			return nil
		},
	}
	return cmd
}

// NewCleanupCmd creates the `cleanup` subcommand.
func NewCleanupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Trigger cleanup of stale deployments",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := NewClientFromCmd(cmd)
			data, status, err := client.Post(cmd.Context(), "/api/v1/cleanup", nil)
			if err != nil {
				return err
			}
			if status != http.StatusAccepted && status != http.StatusOK {
				return fmt.Errorf("server returned %d: %s", status, string(data))
			}

			if IsJSON(cmd) {
				return PrintJSON(json.RawMessage(data))
			}

			fmt.Println("Cleanup triggered.")
			return nil
		},
	}
	return cmd
}

// NewHealthCmd creates the `health` subcommand.
func NewHealthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Check server health",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := NewClientFromCmd(cmd)
			data, status, err := client.Get(cmd.Context(), "/health")
			if err != nil {
				return err
			}
			if status != http.StatusOK && status != http.StatusServiceUnavailable {
				return fmt.Errorf("server returned %d: %s", status, string(data))
			}

			var health HealthResponse
			if err := json.Unmarshal(data, &health); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			if IsJSON(cmd) {
				return PrintJSON(health)
			}

			fmt.Printf("Server:  %s\n", client.BaseURL)
			fmt.Printf("Status:  %s\n", health.Status)
			for name, comp := range health.Components {
				line := fmt.Sprintf("  %s: %s", name, comp.Status)
				if comp.Version != "" {
					line += fmt.Sprintf(" (version: %s)", comp.Version)
				}
				if comp.Error != "" {
					line += fmt.Sprintf(" — %s", comp.Error)
				}
				fmt.Println(line)
			}

			if health.Status != "ok" {
				cmd.SilenceUsage = true
				return fmt.Errorf("server is %s", health.Status)
			}
			return nil
		},
	}
	return cmd
}

func parseAndFormat(s string) string {
	if s == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return FormatTime(t)
}
