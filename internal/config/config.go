package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server        ServerConfig        `yaml:"server"`
	Domain        DomainConfig        `yaml:"domain"`
	GitHub        GitHubConfig        `yaml:"github"`
	Docker        DockerConfig        `yaml:"docker"`
	Defaults      DefaultsConfig      `yaml:"defaults"`
	Apps          map[string]AppConfig `yaml:"apps"`
	Cleanup       CleanupConfig       `yaml:"cleanup"`
	Logging       LoggingConfig       `yaml:"logging"`

	Security      SecurityConfig      `yaml:"security"`
	RateLimit     RateLimitConfig     `yaml:"rate_limit"`
}

type ServerConfig struct {
	Host          string   `yaml:"host"`
	Port          int      `yaml:"port"`
	WebhookSecret string   `yaml:"webhook_secret"`
	EnableAuth    bool     `yaml:"enable_auth"`
	APIKeys       []string `yaml:"api_keys"`
}

type DomainConfig struct {
	BaseDomain string `yaml:"base_domain"`
}

type GitHubConfig struct {
	Token string `yaml:"token"`
	Owner string `yaml:"owner"`
}

type DockerConfig struct {
	Network  string `yaml:"network"`
	Registry string `yaml:"registry"`
}

type DefaultsConfig struct {
	CleanupAfterHours int `yaml:"cleanup_after_hours"`
}

type AppConfig struct {
	Repo            string            `yaml:"repo"`
	Image           string            `yaml:"image"`
	ComposeTemplate string            `yaml:"compose_template"`
	InitFiles       string            `yaml:"init_files"`
	DeployRef       string            `yaml:"deploy_ref"`
	Env             map[string]string `yaml:"env"`
	Scripts         ScriptsConfig     `yaml:"scripts"`
}

type ScriptsConfig struct {
	PreDeploy  string `yaml:"pre_deploy"`
	PostDeploy string `yaml:"post_deploy"`
	PreRemove  string `yaml:"pre_remove"`
	PostRemove string `yaml:"post_remove"`
}

type CleanupConfig struct {
	Schedule       string `yaml:"schedule"`
	OnBranchDelete bool   `yaml:"on_branch_delete"`
	KeepRecent     int    `yaml:"keep_recent"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
	File   string `yaml:"file"`
}

type SecurityConfig struct {
	AllowedInitHosts []string `yaml:"allowed_init_hosts"`
	BlockPrivateIPs  bool     `yaml:"block_private_ips"`
}

type RateLimitConfig struct {
	Enabled           bool `yaml:"enabled"`
	RequestsPerMinute int  `yaml:"requests_per_minute"`
}

// Load reads the configuration from a YAML file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	// Expand environment variables
	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	// Apply defaults
	cfg.applyDefaults()

	// Validate
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server.Host == "" {
		c.Server.Host = "0.0.0.0"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8080
	}
	if c.Docker.Network == "" {
		c.Docker.Network = "web"
	}
	if c.Docker.Registry == "" {
		c.Docker.Registry = "ghcr.io"
	}
	if c.Defaults.CleanupAfterHours == 0 {
		c.Defaults.CleanupAfterHours = 168
	}
	if c.Cleanup.Schedule == "" {
		c.Cleanup.Schedule = "0 * * * *"
	}
	if c.Cleanup.KeepRecent == 0 {
		c.Cleanup.KeepRecent = 3
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}

	// Security defaults
	if len(c.Security.AllowedInitHosts) == 0 {
		c.Security.AllowedInitHosts = []string{"raw.githubusercontent.com"}
	}

	// Rate limit defaults
	if c.RateLimit.RequestsPerMinute == 0 {
		c.RateLimit.RequestsPerMinute = 60
	}

	// Apply defaults to each app
	for name, app := range c.Apps {
		if app.Image == "" {
			app.Image = app.Repo
		}
		c.Apps[name] = app
	}
}

func (c *Config) validate() error {
	if c.Domain.BaseDomain == "" {
		return fmt.Errorf("domain.base_domain is required")
	}
	if c.GitHub.Token == "" {
		return fmt.Errorf("github.token is required")
	}
	if c.GitHub.Owner == "" {
		return fmt.Errorf("github.owner is required")
	}
	if len(c.Apps) == 0 {
		return fmt.Errorf("at least one app must be configured")
	}
	for name, app := range c.Apps {
		if app.Repo == "" {
			return fmt.Errorf("app %s: repo is required", name)
		}
		if app.ComposeTemplate == "" {
			return fmt.Errorf("app %s: compose_template is required", name)
		}
		if _, err := os.Stat(app.ComposeTemplate); err != nil {
			return fmt.Errorf("app %s: compose_template %q not found: %w", name, app.ComposeTemplate, err)
		}
		for label, path := range map[string]string{
			"pre_deploy":  app.Scripts.PreDeploy,
			"post_deploy": app.Scripts.PostDeploy,
			"pre_remove":  app.Scripts.PreRemove,
			"post_remove": app.Scripts.PostRemove,
		} {
			if path != "" {
				if _, err := os.Stat(path); err != nil {
					return fmt.Errorf("app %s: scripts.%s %q not found: %w", name, label, path, err)
				}
			}
		}
	}

	// Security validations
	if c.Server.EnableAuth {
		// Check that API keys are configured when auth is enabled
		hasValidKey := false
		for _, key := range c.Server.APIKeys {
			if key != "" {
				hasValidKey = true
				break
			}
		}
		if !hasValidKey {
			return fmt.Errorf("server.api_keys must have at least one non-empty key when enable_auth is true")
		}
	}

	// Warn about plaintext secrets (tokens starting with known prefixes)
	if isPlaintextToken(c.GitHub.Token) {
		return fmt.Errorf("github.token appears to be a plaintext token; use environment variable syntax ${GITHUB_TOKEN} for security")
	}

	return nil
}

// isPlaintextToken checks if a token looks like a plaintext secret
func isPlaintextToken(token string) bool {
	// GitHub tokens start with these prefixes
	plaintextPrefixes := []string{
		"ghp_",  // Personal access token
		"gho_",  // OAuth access token
		"ghu_",  // User-to-server token
		"ghs_",  // Server-to-server token
		"ghr_",  // Refresh token
		"github_pat_", // Fine-grained PAT
	}

	for _, prefix := range plaintextPrefixes {
		if strings.HasPrefix(token, prefix) {
			return true
		}
	}

	return false
}

// GetAppByRepo finds an app configuration by repository name
func (c *Config) GetAppByRepo(repo string) (string, *AppConfig, bool) {
	for name, app := range c.Apps {
		if app.Repo == repo {
			return name, &app, true
		}
	}
	return "", nil, false
}

// GeneratePassword creates a random password
func GeneratePassword() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generating secure random: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// SanitizeBranchName converts a branch name to a valid subdomain/container name
func SanitizeBranchName(branch string) string {
	// Remove refs/heads/ prefix if present
	branch = strings.TrimPrefix(branch, "refs/heads/")

	// Replace invalid characters
	replacer := strings.NewReplacer(
		"/", "-",
		"_", "-",
		".", "-",
	)
	sanitized := replacer.Replace(branch)

	// Convert to lowercase
	sanitized = strings.ToLower(sanitized)

	// Remove any remaining invalid characters
	var result strings.Builder
	for _, c := range sanitized {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			result.WriteRune(c)
		}
	}

	// Trim leading/trailing hyphens
	sanitized = strings.Trim(result.String(), "-")

	// Limit length (DNS labels max 63 chars)
	if len(sanitized) > 63 {
		sanitized = sanitized[:63]
	}

	return sanitized
}

// DeploymentDir returns the directory for a specific deployment
func (c *Config) DeploymentDir(app, branch string) string {
	return filepath.Join("/var/lib/deployer/deployments", app, SanitizeBranchName(branch))
}
