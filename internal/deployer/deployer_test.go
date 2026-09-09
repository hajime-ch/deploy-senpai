package deployer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/hajime-ch/deploy-senpai/internal/config"
)

// newTestDeployer builds a Deployer rooted at a throwaway data directory that
// already contains a deployment for "myapp" on branch "main".
func newTestDeployer(t *testing.T) (*Deployer, string) {
	t.Helper()

	dataDir := t.TempDir()
	deployDir := filepath.Join(dataDir, "deployments", "myapp", "main")
	if err := os.MkdirAll(deployDir, 0o700); err != nil {
		t.Fatalf("creating deployment dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deployDir, "docker-compose.yml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatalf("writing compose file: %v", err)
	}

	cfg := &config.Config{
		Storage: config.StorageConfig{DataDir: dataDir},
		Apps: map[string]config.AppConfig{
			"myapp": {Repo: "myapp", Image: "myapp"},
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, logger), dataDir
}

func TestRemoveRejectsUnsafeAppName(t *testing.T) {
	unsafe := []string{"..", ".", "../..", "unknown-app"}

	for _, appName := range unsafe {
		t.Run(appName, func(t *testing.T) {
			d, dataDir := newTestDeployer(t)

			err := d.Remove(context.Background(), appName, "main")
			if err == nil {
				t.Fatalf("Remove(%q, \"main\") = nil, want error", appName)
			}

			// The data directory and its contents must survive.
			if _, statErr := os.Stat(filepath.Join(dataDir, "deployments", "myapp", "main", "docker-compose.yml")); statErr != nil {
				t.Errorf("existing deployment was destroyed: %v", statErr)
			}
		})
	}
}

func TestRemoveRejectsBranchThatSanitizesToEmpty(t *testing.T) {
	for _, branch := range []string{"___", "---", "%"} {
		t.Run(branch, func(t *testing.T) {
			d, dataDir := newTestDeployer(t)

			err := d.Remove(context.Background(), "myapp", branch)
			if err == nil {
				t.Fatalf("Remove(\"myapp\", %q) = nil, want error", branch)
			}

			// The app directory must not be collapsed into and deleted.
			appDir := filepath.Join(dataDir, "deployments", "myapp")
			if _, statErr := os.Stat(appDir); statErr != nil {
				t.Errorf("app directory was destroyed: %v", statErr)
			}
		})
	}
}

func TestRemoveDeletesOnlyTheRequestedDeployment(t *testing.T) {
	d, dataDir := newTestDeployer(t)

	other := filepath.Join(dataDir, "deployments", "myapp", "other")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatalf("creating sibling deployment: %v", err)
	}

	if err := d.Remove(context.Background(), "myapp", "main"); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}

	if _, err := os.Stat(filepath.Join(dataDir, "deployments", "myapp", "main")); !os.IsNotExist(err) {
		t.Errorf("requested deployment still present, stat err = %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("sibling deployment was destroyed: %v", err)
	}
}

func TestDeployRejectsUnsafeBranch(t *testing.T) {
	d, _ := newTestDeployer(t)

	unsafe := []string{"main\n    privileged: true", "___", "../../etc"}
	for _, branch := range unsafe {
		t.Run(branch, func(t *testing.T) {
			// Asserting on the sentinel matters here: Deploy would fail anyway on
			// the missing compose template, which would not prove validation ran.
			_, err := d.Deploy(context.Background(), "myapp", branch, "abc1234")
			if !errors.Is(err, config.ErrInvalidRefName) {
				t.Errorf("Deploy(branch=%q) error = %v, want ErrInvalidRefName", branch, err)
			}
		})
	}
}

// Readers (the status endpoint, cleanup) run concurrently with an in-flight
// Deploy, so the accessors must hand out snapshots rather than the live record.
func TestAccessorsReturnSnapshotsNotLiveRecords(t *testing.T) {
	d, _ := newTestDeployer(t)

	started, err := d.StartDeployment("myapp", "main", "0.1.7")
	if err != nil {
		t.Fatalf("StartDeployment: %v", err)
	}

	byKey, ok := d.Get("myapp", "main")
	if !ok {
		t.Fatal("Get did not find the deployment")
	}
	byID, ok := d.GetByID(started.ID)
	if !ok {
		t.Fatal("GetByID did not find the deployment")
	}
	listed := d.List()
	if len(listed) != 1 {
		t.Fatalf("List returned %d deployments, want 1", len(listed))
	}

	d.UpdateDeploymentStatus("myapp", "main", StatusRunning, "")

	for name, dep := range map[string]*Deployment{"Get": byKey, "GetByID": byID, "List": listed[0]} {
		if dep.Status != StatusPending {
			t.Errorf("%s returned a live record: status changed to %q under the caller", name, dep.Status)
		}
	}
}
