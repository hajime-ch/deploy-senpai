package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// Client is an HTTP client for the deploy-senpai server API.
type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// NewClientFromCmd creates a Client from cobra command flags and environment variables.
// Fallback chain: --server flag → DEPLOY_SENPAI_URL env → http://localhost:8080
// Same for: --api-key flag → DEPLOY_SENPAI_API_KEY env → empty
func NewClientFromCmd(cmd *cobra.Command) *Client {
	server, _ := cmd.Flags().GetString("server")
	if server == "" {
		server = os.Getenv("DEPLOY_SENPAI_URL")
	}
	if server == "" {
		server = "http://localhost:8080"
	}

	apiKey, _ := cmd.Flags().GetString("api-key")
	if apiKey == "" {
		apiKey = os.Getenv("DEPLOY_SENPAI_API_KEY")
	}

	return &Client{
		BaseURL: server,
		APIKey:  apiKey,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Get performs a GET request to the given path.
func (c *Client) Get(ctx context.Context, path string) ([]byte, int, error) {
	return c.do(ctx, http.MethodGet, path, nil)
}

// Post performs a POST request to the given path with an optional body.
func (c *Client) Post(ctx context.Context, path string, body io.Reader) ([]byte, int, error) {
	return c.do(ctx, http.MethodPost, path, body)
}

// Delete performs a DELETE request to the given path.
func (c *Client) Delete(ctx context.Context, path string) ([]byte, int, error) {
	return c.do(ctx, http.MethodDelete, path, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) ([]byte, int, error) {
	url := c.BaseURL + path

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("X-API-Key", c.APIKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading response: %w", err)
	}

	return data, resp.StatusCode, nil
}
