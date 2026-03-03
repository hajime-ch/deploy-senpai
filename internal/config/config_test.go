package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeBranchName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple branch",
			input:    "main",
			expected: "main",
		},
		{
			name:     "refs/heads prefix",
			input:    "refs/heads/feature",
			expected: "feature",
		},
		{
			name:     "slash in branch name",
			input:    "feature/login",
			expected: "feature-login",
		},
		{
			name:     "underscore in branch name",
			input:    "feature_login",
			expected: "feature-login",
		},
		{
			name:     "dots in branch name",
			input:    "release.1.0",
			expected: "release-1-0",
		},
		{
			name:     "uppercase letters",
			input:    "Feature/LOGIN",
			expected: "feature-login",
		},
		{
			name:     "special characters",
			input:    "feature@login#test",
			expected: "featurelogintest",
		},
		{
			name:     "leading hyphen",
			input:    "-feature",
			expected: "feature",
		},
		{
			name:     "trailing hyphen",
			input:    "feature-",
			expected: "feature",
		},
		{
			name:     "multiple consecutive hyphens",
			input:    "feature--login",
			expected: "feature--login",
		},
		{
			name:     "long branch name truncation",
			input:    "this-is-a-very-long-branch-name-that-exceeds-the-maximum-allowed-length-for-dns-labels",
			expected: "this-is-a-very-long-branch-name-that-exceeds-the-maximum-allowe",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeBranchName(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeBranchName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGeneratePassword(t *testing.T) {
	// Test that password is generated
	password1, err := GeneratePassword()
	if err != nil {
		t.Fatalf("GeneratePassword() returned error: %v", err)
	}

	if len(password1) != 32 { // 16 bytes = 32 hex chars
		t.Errorf("GeneratePassword() length = %d, want 32", len(password1))
	}

	// Test uniqueness
	password2, err := GeneratePassword()
	if err != nil {
		t.Fatalf("GeneratePassword() returned error: %v", err)
	}

	if password1 == password2 {
		t.Error("GeneratePassword() should generate unique passwords")
	}

	// Test that password contains only hex characters
	for _, c := range password1 {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Errorf("GeneratePassword() contains non-hex character: %c", c)
		}
	}
}

func TestIsPlaintextToken(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		expected bool
	}{
		{
			name:     "personal access token",
			token:    "ghp_xxxxxxxxxxxxxxxxxxxx",
			expected: true,
		},
		{
			name:     "OAuth token",
			token:    "gho_xxxxxxxxxxxxxxxxxxxx",
			expected: true,
		},
		{
			name:     "fine-grained PAT",
			token:    "github_pat_xxxxxxxxxxxxxxxxxxxx",
			expected: true,
		},
		{
			name:     "environment variable syntax",
			token:    "${GITHUB_TOKEN}",
			expected: false,
		},
		{
			name:     "empty string",
			token:    "",
			expected: false,
		},
		{
			name:     "random string",
			token:    "someothertoken",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPlaintextToken(tt.token)
			if result != tt.expected {
				t.Errorf("isPlaintextToken(%q) = %v, want %v", tt.token, result, tt.expected)
			}
		})
	}
}

func TestConfigLoad(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Create a compose template file for the test app
	tplPath := filepath.Join(tmpDir, "compose.yml.tpl")
	if err := os.WriteFile(tplPath, []byte("services:\n  app:\n    image: {{.Image}}\n"), 0644); err != nil {
		t.Fatalf("Failed to write template file: %v", err)
	}

	configContent := `
domain:
  base_domain: "staging.example.com"
github:
  token: "${GITHUB_TOKEN}"
  owner: "test-org"
apps:
  test-app:
    repo: "test-repo"
    compose_template: "` + tplPath + `"
`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Set environment variable
	t.Setenv("GITHUB_TOKEN", "test-token")

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.Domain.BaseDomain != "staging.example.com" {
		t.Errorf("BaseDomain = %q, want %q", cfg.Domain.BaseDomain, "staging.example.com")
	}

	if cfg.GitHub.Token != "test-token" {
		t.Errorf("Token = %q, want %q", cfg.GitHub.Token, "test-token")
	}

	if cfg.GitHub.Owner != "test-org" {
		t.Errorf("Owner = %q, want %q", cfg.GitHub.Owner, "test-org")
	}
}

func TestConfigValidation(t *testing.T) {
	// Create a temp compose template for valid tests
	tmpDir := t.TempDir()
	tplPath := filepath.Join(tmpDir, "compose.yml.tpl")
	if err := os.WriteFile(tplPath, []byte("services:\n  app:\n    image: test\n"), 0644); err != nil {
		t.Fatalf("Failed to write template file: %v", err)
	}

	tests := []struct {
		name        string
		config      Config
		expectError bool
	}{
		{
			name: "valid config",
			config: Config{
				Domain: DomainConfig{BaseDomain: "example.com"},
				GitHub: GitHubConfig{Token: "${TOKEN}", Owner: "org"},
				Apps:   map[string]AppConfig{"app": {Repo: "repo", ComposeTemplate: tplPath}},
			},
			expectError: false,
		},
		{
			name: "missing base domain with app that lacks its own",
			config: Config{
				GitHub: GitHubConfig{Token: "${TOKEN}", Owner: "org"},
				Apps:   map[string]AppConfig{"app": {Repo: "repo", ComposeTemplate: tplPath}},
			},
			expectError: true,
		},
		{
			name: "no global base domain but all apps have their own",
			config: Config{
				GitHub: GitHubConfig{Token: "${TOKEN}", Owner: "org"},
				Apps:   map[string]AppConfig{"app": {Repo: "repo", ComposeTemplate: tplPath, BaseDomain: "app.example.com"}},
			},
			expectError: false,
		},
		{
			name: "no global base domain with mixed apps",
			config: Config{
				GitHub: GitHubConfig{Token: "${TOKEN}", Owner: "org"},
				Apps: map[string]AppConfig{
					"a": {Repo: "r1", ComposeTemplate: tplPath, BaseDomain: "a.example.com"},
					"b": {Repo: "r2", ComposeTemplate: tplPath},
				},
			},
			expectError: true,
		},
		{
			name: "missing github token",
			config: Config{
				Domain: DomainConfig{BaseDomain: "example.com"},
				GitHub: GitHubConfig{Owner: "org"},
				Apps:   map[string]AppConfig{"app": {Repo: "repo", ComposeTemplate: tplPath}},
			},
			expectError: true,
		},
		// plaintext token detection is tested in TestLoadPlaintextToken
		// since it now runs on raw YAML before env expansion
		{
			name: "no apps",
			config: Config{
				Domain: DomainConfig{BaseDomain: "example.com"},
				GitHub: GitHubConfig{Token: "${TOKEN}", Owner: "org"},
				Apps:   map[string]AppConfig{},
			},
			expectError: true,
		},
		{
			name: "app missing repo",
			config: Config{
				Domain: DomainConfig{BaseDomain: "example.com"},
				GitHub: GitHubConfig{Token: "${TOKEN}", Owner: "org"},
				Apps:   map[string]AppConfig{"app": {ComposeTemplate: tplPath}},
			},
			expectError: true,
		},
		{
			name: "app missing compose_template",
			config: Config{
				Domain: DomainConfig{BaseDomain: "example.com"},
				GitHub: GitHubConfig{Token: "${TOKEN}", Owner: "org"},
				Apps:   map[string]AppConfig{"app": {Repo: "repo"}},
			},
			expectError: true,
		},
		{
			name: "app compose_template not found",
			config: Config{
				Domain: DomainConfig{BaseDomain: "example.com"},
				GitHub: GitHubConfig{Token: "${TOKEN}", Owner: "org"},
				Apps:   map[string]AppConfig{"app": {Repo: "repo", ComposeTemplate: "/nonexistent/path.tpl"}},
			},
			expectError: true,
		},
		{
			name: "auth enabled without keys",
			config: Config{
				Domain: DomainConfig{BaseDomain: "example.com"},
				GitHub: GitHubConfig{Token: "${TOKEN}", Owner: "org"},
				Server: ServerConfig{EnableAuth: true, APIKeys: []string{}},
				Apps:   map[string]AppConfig{"app": {Repo: "repo", ComposeTemplate: tplPath}},
			},
			expectError: true,
		},
		{
			name: "auth enabled with empty key",
			config: Config{
				Domain: DomainConfig{BaseDomain: "example.com"},
				GitHub: GitHubConfig{Token: "${TOKEN}", Owner: "org"},
				Server: ServerConfig{EnableAuth: true, APIKeys: []string{""}},
				Apps:   map[string]AppConfig{"app": {Repo: "repo", ComposeTemplate: tplPath}},
			},
			expectError: true,
		},
		{
			name: "script path not found",
			config: Config{
				Domain: DomainConfig{BaseDomain: "example.com"},
				GitHub: GitHubConfig{Token: "${TOKEN}", Owner: "org"},
				Apps: map[string]AppConfig{"app": {
					Repo:            "repo",
					ComposeTemplate: tplPath,
					Scripts:         ScriptsConfig{PreDeploy: "/nonexistent/script.sh"},
				}},
			},
			expectError: true,
		},
		{
			name: "empty scripts pass validation",
			config: Config{
				Domain: DomainConfig{BaseDomain: "example.com"},
				GitHub: GitHubConfig{Token: "${TOKEN}", Owner: "org"},
				Apps: map[string]AppConfig{"app": {
					Repo:            "repo",
					ComposeTemplate: tplPath,
					Scripts:         ScriptsConfig{},
				}},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.validate()
			if tt.expectError && err == nil {
				t.Error("validate() expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("validate() unexpected error: %v", err)
			}
		})
	}
}

func TestLoadPlaintextToken(t *testing.T) {
	tmpDir := t.TempDir()
	tplPath := filepath.Join(tmpDir, "compose.yml.tpl")
	if err := os.WriteFile(tplPath, []byte("services:\n  app:\n    image: test\n"), 0644); err != nil {
		t.Fatalf("Failed to write template file: %v", err)
	}

	tests := []struct {
		name        string
		token       string
		expectError bool
	}{
		{"plaintext ghp_ token", "ghp_xxxxxxxxxxxx", true},
		{"plaintext github_pat_ token", "github_pat_xxxxxxxxxxxx", true},
		{"env var reference", "${GITHUB_TOKEN}", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configContent := `
domain:
  base_domain: "example.com"
github:
  token: "` + tt.token + `"
  owner: "org"
apps:
  app:
    repo: "repo"
    compose_template: "` + tplPath + `"
`
			configPath := filepath.Join(tmpDir, "config-"+tt.name+".yaml")
			if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
				t.Fatalf("Failed to write config file: %v", err)
			}

			t.Setenv("GITHUB_TOKEN", "test-token-value")

			_, err := Load(configPath)
			if tt.expectError && err == nil {
				t.Error("Load() expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Load() unexpected error: %v", err)
			}
		})
	}
}

func TestBaseDomainForApp(t *testing.T) {
	cfg := &Config{
		Domain: DomainConfig{BaseDomain: "global.example.com"},
		Apps: map[string]AppConfig{
			"with-override": {Repo: "r1", BaseDomain: "custom.example.com"},
			"without":       {Repo: "r2"},
		},
	}

	tests := []struct {
		name     string
		app      string
		expected string
	}{
		{"per-app override", "with-override", "custom.example.com"},
		{"fallback to global", "without", "global.example.com"},
		{"unknown app falls back to global", "nonexistent", "global.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cfg.BaseDomainForApp(tt.app)
			if got != tt.expected {
				t.Errorf("BaseDomainForApp(%q) = %q, want %q", tt.app, got, tt.expected)
			}
		})
	}
}

func TestGetAppByRepo(t *testing.T) {
	cfg := &Config{
		Apps: map[string]AppConfig{
			"app1": {Repo: "repo1", ComposeTemplate: "/tmp/test.tpl"},
			"app2": {Repo: "repo2", ComposeTemplate: "/tmp/test.tpl"},
		},
	}

	// Test found
	name, app, found := cfg.GetAppByRepo("repo1")
	if !found {
		t.Error("GetAppByRepo() should find repo1")
	}
	if name != "app1" {
		t.Errorf("GetAppByRepo() name = %q, want %q", name, "app1")
	}
	if app.Repo != "repo1" {
		t.Errorf("GetAppByRepo() repo = %q, want %q", app.Repo, "repo1")
	}

	// Test not found
	_, _, found = cfg.GetAppByRepo("unknown")
	if found {
		t.Error("GetAppByRepo() should not find unknown repo")
	}
}
