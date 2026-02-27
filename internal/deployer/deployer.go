package deployer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hajime-ch/deploy-senpai/internal/config"
)

// DeploymentStatus represents the current state of a deployment
type DeploymentStatus string

const (
	StatusPending    DeploymentStatus = "pending"
	StatusInProgress DeploymentStatus = "in_progress"
	StatusRunning    DeploymentStatus = "running"
	StatusFailed     DeploymentStatus = "failed"
	StatusStopped    DeploymentStatus = "stopped"
)

// Deployment represents a running deployment
type Deployment struct {
	ID            string           `json:"id"`
	App           string           `json:"app"`
	Branch        string           `json:"branch"`
	SanitizedName string           `json:"sanitized_name"`
	URL           string           `json:"url"`
	ImageTag      string           `json:"image_tag"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
	LastActivity  time.Time        `json:"last_activity"`
	Status        DeploymentStatus `json:"status"`
	ErrorMessage  string           `json:"error_message,omitempty"`
}

// Deployer handles container deployments
type Deployer struct {
	cfg              *config.Config
	logger           *slog.Logger
	deployments      map[string]*Deployment // key: "app/branch"
	mu               sync.RWMutex
	dataDir          string
	stateManager     *StateManager
	templateRenderer *TemplateRenderer
}

// New creates a new Deployer
func New(cfg *config.Config, logger *slog.Logger) *Deployer {
	dataDir := "/var/lib/deployer"

	// Get encryption key from environment (32 bytes for AES-256)
	var encryptionKey []byte
	if key := os.Getenv("DEPLOYER_ENCRYPTION_KEY"); key != "" {
		encryptionKey = []byte(key)
		if len(encryptionKey) != 32 {
			logger.Warn("DEPLOYER_ENCRYPTION_KEY should be 32 bytes for AES-256, passwords will not be encrypted")
			encryptionKey = nil
		}
	}

	stateManager := NewStateManager(dataDir, encryptionKey)

	d := &Deployer{
		cfg:              cfg,
		logger:           logger,
		deployments:      make(map[string]*Deployment),
		dataDir:          dataDir,
		stateManager:     stateManager,
		templateRenderer: NewTemplateRenderer(),
	}

	// Load existing deployments from disk
	d.loadDeployments()

	return d
}

// generateDeploymentID creates a unique deployment ID
func generateDeploymentID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// StartDeployment creates a pending deployment record and returns its ID
// This should be called before starting async deployment
func (d *Deployer) StartDeployment(appName, branch, imageTag string) (*Deployment, error) {
	appCfg, ok := d.cfg.Apps[appName]
	if !ok {
		return nil, fmt.Errorf("unknown app: %s", appName)
	}

	// Use deploy_ref as the slot name when configured (e.g. "production"),
	// otherwise use the branch/tag name.
	slotName := branch
	if appCfg.DeployRef != "" {
		slotName = appCfg.DeployRef
	}
	sanitized := config.SanitizeBranchName(slotName)
	key := fmt.Sprintf("%s/%s", appName, sanitized)
	subdomain := fmt.Sprintf("%s-%s", appName, sanitized)

	now := time.Now()
	deployment := &Deployment{
		ID:            generateDeploymentID(),
		App:           appName,
		Branch:        branch,
		SanitizedName: sanitized,
		URL:           fmt.Sprintf("https://%s.%s", subdomain, d.cfg.Domain.BaseDomain),
		ImageTag:      imageTag,
		CreatedAt:     now,
		UpdatedAt:     now,
		LastActivity:  now,
		Status:        StatusPending,
	}

	d.mu.Lock()
	d.deployments[key] = deployment
	d.mu.Unlock()

	return deployment, nil
}

// UpdateDeploymentStatus updates the status of a deployment
func (d *Deployer) UpdateDeploymentStatus(appName, branch string, status DeploymentStatus, errMsg string) {
	sanitized := config.SanitizeBranchName(branch)
	key := fmt.Sprintf("%s/%s", appName, sanitized)

	d.mu.Lock()
	defer d.mu.Unlock()

	if dep, ok := d.deployments[key]; ok {
		dep.Status = status
		dep.UpdatedAt = time.Now()
		dep.LastActivity = time.Now()
		if errMsg != "" {
			dep.ErrorMessage = errMsg
		}
		d.saveDeployment(dep, nil)
	}
}

// GetByID returns a deployment by its ID
func (d *Deployer) GetByID(id string) (*Deployment, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	for _, dep := range d.deployments {
		if dep.ID == id {
			return dep, true
		}
	}
	return nil, false
}

// Deploy creates or updates a deployment for a branch
func (d *Deployer) Deploy(ctx context.Context, appName, branch, imageTag string) (*Deployment, error) {
	appCfg, ok := d.cfg.Apps[appName]
	if !ok {
		return nil, fmt.Errorf("unknown app: %s", appName)
	}

	// Use deploy_ref as the slot name when configured (e.g. "production"),
	// otherwise use the branch/tag name.
	slotName := branch
	if appCfg.DeployRef != "" {
		slotName = appCfg.DeployRef
	}
	sanitized := config.SanitizeBranchName(slotName)
	key := fmt.Sprintf("%s/%s", appName, sanitized)
	deployDir := filepath.Join(d.dataDir, "deployments", appName, sanitized)
	subdomain := fmt.Sprintf("%s-%s", appName, sanitized)

	// Check if we already have a pending deployment
	d.mu.RLock()
	existingDep, exists := d.deployments[key]
	d.mu.RUnlock()

	var deployment *Deployment
	if exists && existingDep.Status == StatusPending {
		deployment = existingDep
	} else {
		// Create new deployment record
		now := time.Now()
		deployment = &Deployment{
			ID:            generateDeploymentID(),
			App:           appName,
			Branch:        branch,
			SanitizedName: sanitized,
			URL:           fmt.Sprintf("https://%s.%s", subdomain, d.cfg.Domain.BaseDomain),
			ImageTag:      imageTag,
			CreatedAt:     now,
			UpdatedAt:     now,
			LastActivity:  now,
			Status:        StatusPending,
		}
	}

	// Update status to in_progress
	deployment.Status = StatusInProgress
	deployment.UpdatedAt = time.Now()
	d.mu.Lock()
	d.deployments[key] = deployment
	d.mu.Unlock()

	d.logger.Info("starting deployment",
		"id", deployment.ID,
		"app", appName,
		"branch", branch,
		"sanitized", sanitized,
		"image_tag", imageTag,
	)

	// Helper to mark failure
	markFailed := func(err error) (*Deployment, error) {
		deployment.Status = StatusFailed
		deployment.ErrorMessage = err.Error()
		deployment.UpdatedAt = time.Now()
		d.mu.Lock()
		d.deployments[key] = deployment
		d.mu.Unlock()
		d.saveDeployment(deployment, nil)
		return deployment, err
	}

	// Create deployment directory
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		return markFailed(fmt.Errorf("creating deployment directory: %w", err))
	}

	// Copy init files if configured
	if appCfg.InitFiles != "" {
		if err := copyInitFiles(appCfg.InitFiles, deployDir); err != nil {
			d.logger.Warn("failed to copy init files", "error", err)
		}
	}

	// Run pre-deploy script
	if err := d.runScript(ctx, appCfg.Scripts.PreDeploy, deployment); err != nil {
		return markFailed(fmt.Errorf("pre-deploy script: %w", err))
	}

	// Load existing passwords from metadata
	existingPasswords, err := d.stateManager.LoadPasswords(appName, sanitized)
	if err != nil {
		d.logger.Warn("failed to load existing passwords", "error", err)
	}
	if existingPasswords == nil {
		existingPasswords = make(map[string]string)
	}

	// Build full image reference
	image := fmt.Sprintf("%s/%s/%s:%s", d.cfg.Docker.Registry, d.cfg.GitHub.Owner, appCfg.Image, imageTag)

	// Render user template
	templateData := TemplateData{
		AppName:         appName,
		Branch:          branch,
		SanitizedBranch: sanitized,
		ImageTag:        imageTag,
		Image:           image,
		Registry:        d.cfg.Docker.Registry,
		Owner:           d.cfg.GitHub.Owner,
		BaseDomain:      d.cfg.Domain.BaseDomain,
		Subdomain:       subdomain,
		Network:         d.cfg.Docker.Network,
		Env:             appCfg.Env,
	}

	composeFile := filepath.Join(deployDir, "docker-compose.yml")
	passwords, err := d.templateRenderer.RenderTemplate(appCfg.ComposeTemplate, templateData, existingPasswords, composeFile)
	if err != nil {
		return markFailed(fmt.Errorf("rendering compose template: %w", err))
	}

	// Login to registry
	if err := d.loginToRegistry(ctx); err != nil {
		return markFailed(fmt.Errorf("registry login: %w", err))
	}

	// Pull the image
	if err := d.pullImage(ctx, &appCfg, imageTag); err != nil {
		return markFailed(fmt.Errorf("pulling image: %w", err))
	}

	// Start/update containers
	if err := d.runCompose(ctx, deployDir, "up", "-d", "--remove-orphans"); err != nil {
		return markFailed(fmt.Errorf("starting containers: %w", err))
	}

	// Mark as running
	deployment.Status = StatusRunning
	deployment.UpdatedAt = time.Now()
	deployment.LastActivity = time.Now()
	deployment.ErrorMessage = ""

	d.mu.Lock()
	d.deployments[key] = deployment
	d.mu.Unlock()

	// Save deployment info with passwords
	d.saveDeployment(deployment, passwords)

	d.logger.Info("deployment successful",
		"id", deployment.ID,
		"app", appName,
		"branch", branch,
		"url", deployment.URL,
	)

	// Run post-deploy script (failure is non-fatal)
	if err := d.runScript(ctx, appCfg.Scripts.PostDeploy, deployment); err != nil {
		d.logger.Warn("post-deploy script failed", "error", err)
	}

	return deployment, nil
}

// Remove tears down a deployment
func (d *Deployer) Remove(ctx context.Context, appName, branch string) error {
	sanitized := config.SanitizeBranchName(branch)
	key := fmt.Sprintf("%s/%s", appName, sanitized)
	deployDir := filepath.Join(d.dataDir, "deployments", appName, sanitized)

	d.logger.Info("removing deployment", "app", appName, "branch", branch)

	// Run pre-remove script
	if appCfg, ok := d.cfg.Apps[appName]; ok {
		d.mu.RLock()
		dep := d.deployments[key]
		d.mu.RUnlock()
		if dep != nil {
			if err := d.runScript(ctx, appCfg.Scripts.PreRemove, dep); err != nil {
				return fmt.Errorf("pre-remove script: %w", err)
			}
		}
	}

	// Stop containers
	if err := d.runCompose(ctx, deployDir, "down", "-v", "--remove-orphans"); err != nil {
		d.logger.Warn("error stopping containers", "error", err)
	}

	// Remove deployment directory
	if err := os.RemoveAll(deployDir); err != nil {
		d.logger.Warn("failed to remove deployment directory", "error", err)
	}

	d.mu.Lock()
	delete(d.deployments, key)
	d.mu.Unlock()

	d.logger.Info("deployment removed", "app", appName, "branch", branch)

	// Run post-remove script (failure is non-fatal)
	if appCfg, ok := d.cfg.Apps[appName]; ok {
		dep := &Deployment{App: appName, Branch: branch, SanitizedName: sanitized}
		if err := d.runScript(ctx, appCfg.Scripts.PostRemove, dep); err != nil {
			d.logger.Warn("post-remove script failed", "error", err)
		}
	}

	return nil
}

// List returns all active deployments
func (d *Deployer) List() []*Deployment {
	d.mu.RLock()
	defer d.mu.RUnlock()

	result := make([]*Deployment, 0, len(d.deployments))
	for _, dep := range d.deployments {
		result = append(result, dep)
	}
	return result
}

// Get returns a specific deployment
func (d *Deployer) Get(appName, branch string) (*Deployment, bool) {
	sanitized := config.SanitizeBranchName(branch)
	key := fmt.Sprintf("%s/%s", appName, sanitized)

	d.mu.RLock()
	defer d.mu.RUnlock()

	dep, ok := d.deployments[key]
	return dep, ok
}

// copyInitFiles copies all files from srcDir into deployDir.
func copyInitFiles(srcDir, deployDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("reading init files directory %s: %w", srcDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(deployDir, entry.Name())

		src, err := os.Open(srcPath)
		if err != nil {
			return fmt.Errorf("opening %s: %w", srcPath, err)
		}

		dst, err := os.Create(dstPath)
		if err != nil {
			_ = src.Close()
			return fmt.Errorf("creating %s: %w", dstPath, err)
		}

		_, err = io.Copy(dst, src)
		_ = src.Close()
		_ = dst.Close()
		if err != nil {
			return fmt.Errorf("copying %s: %w", entry.Name(), err)
		}
	}

	return nil
}

// loginToRegistry authenticates with the container registry
func (d *Deployer) loginToRegistry(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "login", d.cfg.Docker.Registry,
		"-u", d.cfg.GitHub.Owner,
		"--password-stdin")
	cmd.Stdin = strings.NewReader(d.cfg.GitHub.Token)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker login failed: %s: %w", output, err)
	}
	return nil
}

// pullImage pulls the container image
func (d *Deployer) pullImage(ctx context.Context, app *config.AppConfig, tag string) error {
	image := fmt.Sprintf("%s/%s/%s:%s", d.cfg.Docker.Registry, d.cfg.GitHub.Owner, app.Image, tag)

	cmd := exec.CommandContext(ctx, "docker", "pull", image)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker pull failed: %s: %w", output, err)
	}
	return nil
}

// runCompose executes docker-compose commands
func (d *Deployer) runCompose(ctx context.Context, dir string, args ...string) error {
	cmdArgs := append([]string{"-f", filepath.Join(dir, "docker-compose.yml")}, args...)
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose"}, cmdArgs...)...)
	cmd.Dir = dir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose %s failed: %s: %w", args[0], output, err)
	}
	return nil
}

// loadDeployments loads existing deployments from disk
func (d *Deployer) loadDeployments() {
	deploymentsDir := filepath.Join(d.dataDir, "deployments")

	apps, err := os.ReadDir(deploymentsDir)
	if err != nil {
		return
	}

	for _, app := range apps {
		if !app.IsDir() {
			continue
		}

		branches, err := os.ReadDir(filepath.Join(deploymentsDir, app.Name()))
		if err != nil {
			continue
		}

		for _, branch := range branches {
			if !branch.IsDir() {
				continue
			}

			// Check if docker-compose exists
			composeFile := filepath.Join(deploymentsDir, app.Name(), branch.Name(), "docker-compose.yml")
			if _, err := os.Stat(composeFile); err != nil {
				continue
			}

			key := fmt.Sprintf("%s/%s", app.Name(), branch.Name())
			subdomain := fmt.Sprintf("%s-%s", app.Name(), branch.Name())

			// Try to load from metadata file first
			metadata, err := d.stateManager.LoadDeployment(app.Name(), branch.Name())
			if err != nil {
				d.logger.Warn("failed to load deployment metadata", "app", app.Name(), "branch", branch.Name(), "error", err)
			}

			if metadata != nil {
				// Parse timestamps
				createdAt, _ := time.Parse("2006-01-02T15:04:05Z07:00", metadata.CreatedAt)
				updatedAt, _ := time.Parse("2006-01-02T15:04:05Z07:00", metadata.UpdatedAt)

				d.deployments[key] = &Deployment{
					ID:            metadata.ID,
					App:           metadata.App,
					Branch:        metadata.Branch,
					SanitizedName: metadata.SanitizedName,
					URL:           metadata.URL,
					ImageTag:      metadata.ImageTag,
					CreatedAt:     createdAt,
					UpdatedAt:     updatedAt,
					LastActivity:  updatedAt,
					Status:        metadata.Status,
					ErrorMessage:  metadata.ErrorMessage,
				}
			} else {
				// Fallback for deployments without metadata
				d.deployments[key] = &Deployment{
					ID:            generateDeploymentID(),
					App:           app.Name(),
					Branch:        branch.Name(),
					SanitizedName: branch.Name(),
					URL:           fmt.Sprintf("https://%s.%s", subdomain, d.cfg.Domain.BaseDomain),
					Status:        StatusRunning, // Assume running if compose exists
					LastActivity:  time.Now(),
					CreatedAt:     time.Now(),
					UpdatedAt:     time.Now(),
				}
			}
		}
	}

	d.logger.Info("loaded existing deployments", "count", len(d.deployments))
}

// saveDeployment persists deployment info to disk.
// passwords may be nil for status-only updates (existing passwords are preserved).
func (d *Deployer) saveDeployment(dep *Deployment, passwords map[string]string) {
	if err := d.stateManager.SaveDeployment(dep, passwords); err != nil {
		d.logger.Error("failed to save deployment metadata", "error", err)
	}
}
