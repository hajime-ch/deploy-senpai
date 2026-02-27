package cleanup

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/hajime-ch/deploy-senpai/internal/config"
	"github.com/hajime-ch/deploy-senpai/internal/deployer"
)

// Cleaner handles automatic cleanup of stale deployments
type Cleaner struct {
	cfg      *config.Config
	deployer *deployer.Deployer
	logger   *slog.Logger
	cron     *cron.Cron
}

// New creates a new Cleaner
func New(cfg *config.Config, d *deployer.Deployer, logger *slog.Logger) *Cleaner {
	return &Cleaner{
		cfg:      cfg,
		deployer: d,
		logger:   logger,
		cron:     cron.New(),
	}
}

// Start begins the cleanup scheduler
func (c *Cleaner) Start() error {
	_, err := c.cron.AddFunc(c.cfg.Cleanup.Schedule, func() {
		c.Run(context.Background())
	})
	if err != nil {
		return err
	}

	c.cron.Start()
	c.logger.Info("cleanup scheduler started", "schedule", c.cfg.Cleanup.Schedule)
	return nil
}

// Stop halts the cleanup scheduler
func (c *Cleaner) Stop() {
	c.cron.Stop()
}

// Run performs a cleanup cycle
func (c *Cleaner) Run(ctx context.Context) {
	c.logger.Info("starting cleanup cycle")

	deployments := c.deployer.List()
	if len(deployments) == 0 {
		c.logger.Info("no deployments to clean up")
		return
	}

	// Group deployments by app
	byApp := make(map[string][]*deployer.Deployment)
	for _, d := range deployments {
		byApp[d.App] = append(byApp[d.App], d)
	}

	now := time.Now()
	maxAge := time.Duration(c.cfg.Defaults.CleanupAfterHours) * time.Hour
	cleaned := 0

	for appName, appDeployments := range byApp {
		// Sort by last activity (newest first)
		sort.Slice(appDeployments, func(i, j int) bool {
			return appDeployments[i].LastActivity.After(appDeployments[j].LastActivity)
		})

		for i, dep := range appDeployments {
			// Always keep the most recent N deployments
			if i < c.cfg.Cleanup.KeepRecent {
				continue
			}

			// Skip if app has custom cleanup settings
			appCfg, ok := c.cfg.Apps[appName]
			if ok {
				// Apps with deploy_ref are long-lived (e.g. production) — never auto-clean
				if appCfg.DeployRef != "" {
					continue
				}
				// Check if this is a protected branch (e.g., main, develop)
				if isProtectedBranch(dep.Branch) {
					continue
				}
			}

			// Check if deployment is stale
			age := now.Sub(dep.LastActivity)
			if age > maxAge {
				c.logger.Info("removing stale deployment",
					"app", dep.App,
					"branch", dep.Branch,
					"age_hours", age.Hours(),
				)

				if err := c.deployer.Remove(ctx, dep.App, dep.Branch); err != nil {
					c.logger.Error("failed to remove deployment",
						"app", dep.App,
						"branch", dep.Branch,
						"error", err,
					)
					continue
				}
				cleaned++
			}
		}
	}

	c.logger.Info("cleanup cycle completed", "removed", cleaned)
}

// CleanupBranch removes a specific branch deployment (called when branch is deleted)
func (c *Cleaner) CleanupBranch(ctx context.Context, appName, branch string) error {
	c.logger.Info("cleaning up deleted branch", "app", appName, "branch", branch)
	return c.deployer.Remove(ctx, appName, branch)
}

// isProtectedBranch checks if a branch should never be automatically cleaned up
func isProtectedBranch(branch string) bool {
	protected := []string{
		"main",
		"master",
		"develop",
		"staging",
		"production",
	}

	for _, p := range protected {
		if branch == p {
			return true
		}
	}
	return false
}
