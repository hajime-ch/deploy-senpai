package deployer

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

const scriptTimeout = 60 * time.Second

// runScript executes a lifecycle script with deployment context as environment variables.
// It is a no-op when scriptPath is empty.
func (d *Deployer) runScript(ctx context.Context, scriptPath string, dep *Deployment) error {
	if scriptPath == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, scriptTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, scriptPath)
	cmd.Env = append(cmd.Environ(),
		"DEPLOY_APP="+dep.App,
		"DEPLOY_BRANCH="+dep.Branch,
		"DEPLOY_SANITIZED_BRANCH="+dep.SanitizedName,
		"DEPLOY_URL="+dep.URL,
		"DEPLOY_IMAGE_TAG="+dep.ImageTag,
		"DEPLOY_ID="+dep.ID,
		"DEPLOY_STATUS="+string(dep.Status),
	)

	output, err := cmd.CombinedOutput()
	if len(output) > 0 {
		d.logger.Info("script output", "script", scriptPath, "output", string(output))
	}
	if err != nil {
		return fmt.Errorf("script %s failed: %w", scriptPath, err)
	}
	return nil
}
