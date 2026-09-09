package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hajime-ch/deploy-senpai/internal/config"
	"github.com/hajime-ch/deploy-senpai/internal/deployer"
)

// newTestServer builds a Server with auth disabled over a throwaway data
// directory holding one deployment: myapp/main.
func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()

	dataDir := t.TempDir()
	deployDir := filepath.Join(dataDir, "deployments", "myapp", "main")
	if err := os.MkdirAll(deployDir, 0o700); err != nil {
		t.Fatalf("creating deployment dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deployDir, "docker-compose.yml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatalf("writing compose file: %v", err)
	}

	authDisabled := false
	cfg := &config.Config{
		Server:  config.ServerConfig{EnableAuth: &authDisabled},
		Storage: config.StorageConfig{DataDir: dataDir},
		Apps: map[string]config.AppConfig{
			"myapp": {Repo: "myapp", Image: "myapp"},
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, deployer.New(cfg, logger), nil, logger)
	t.Cleanup(srv.Stop)

	return srv, dataDir
}

func TestRemoveRejectsTraversalInAppParam(t *testing.T) {
	targets := []string{
		"/api/v1/deployments/../__",
		"/api/v1/deployments/../myapp",
		"/api/v1/deployments/./__",
	}

	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			srv, dataDir := newTestServer(t)

			req := httptest.NewRequest(http.MethodDelete, target, nil)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code >= 200 && rec.Code < 300 {
				t.Errorf("DELETE %s = %d, want a client error", target, rec.Code)
			}
			if _, err := os.Stat(filepath.Join(dataDir, "deployments", "myapp", "main")); err != nil {
				t.Errorf("existing deployment was destroyed: %v", err)
			}
		})
	}
}

func TestRemoveRejectsUnknownAppWithNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/deployments/nosuchapp/main", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE unknown app = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestRemoveRejectsInvalidBranchWithBadRequest(t *testing.T) {
	srv, dataDir := newTestServer(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/deployments/myapp/___", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("DELETE invalid branch = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "deployments", "myapp")); err != nil {
		t.Errorf("app directory was destroyed: %v", err)
	}
}

func TestDeployRejectsInvalidBranchWithBadRequest(t *testing.T) {
	// %0A decodes to a literal newline, which would otherwise be rendered
	// straight into the generated docker-compose.yml.
	targets := []string{
		"/api/v1/deployments/myapp/main%0A____privileged:_true",
		"/api/v1/deployments/myapp/___",
	}

	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			srv, _ := newTestServer(t)

			req := httptest.NewRequest(http.MethodPost, target, nil)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("POST %s = %d, want %d", target, rec.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestDeployRespondsWithoutWaitingForTheDeploy(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/myapp/main", strings.NewReader(`{"image_tag":"0.1.7"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var got struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("parsing response: %v (body: %s)", err, rec.Body.String())
	}
	if got.ID == "" {
		t.Error("response has no deployment id to poll with")
	}
	// The handler must answer before Deploy runs, so the record it reports is
	// still pending rather than a terminal state.
	if got.Status != "pending" {
		t.Errorf("status = %q, want %q", got.Status, "pending")
	}
}

func TestDeployReturnsAnIDThatTheStatusEndpointResolves(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/myapp/main", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	var triggered struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &triggered); err != nil {
		t.Fatalf("parsing response: %v (body: %s)", err, rec.Body.String())
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/status/"+triggered.ID, nil)
	statusRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(statusRec, statusReq)

	if statusRec.Code != http.StatusOK {
		t.Fatalf("status endpoint returned %d, want %d (body: %s)", statusRec.Code, http.StatusOK, statusRec.Body.String())
	}

	var polled struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &polled); err != nil {
		t.Fatalf("parsing status response: %v", err)
	}
	if polled.ID != triggered.ID {
		t.Errorf("status endpoint returned id %q, want %q", polled.ID, triggered.ID)
	}
}

// Clients poll by ID (the only lookup that works for apps pinned to a
// deploy_ref slot), so that response must carry the whole record, not a subset.
func TestStatusEndpointReturnsTheFullDeploymentRecord(t *testing.T) {
	srv, _ := newTestServer(t)

	dep, err := srv.deployer.StartDeployment("myapp", "main", "0.1.7")
	if err != nil {
		t.Fatalf("StartDeployment: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/status/"+dep.ID, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got struct {
		ID       string `json:"id"`
		ImageTag string `json:"image_tag"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("parsing response: %v", err)
	}
	if got.ID != dep.ID {
		t.Errorf("id = %q, want %q", got.ID, dep.ID)
	}
	if got.ImageTag != "0.1.7" {
		t.Errorf("image_tag = %q, want %q", got.ImageTag, "0.1.7")
	}
	if got.Status != "pending" {
		t.Errorf("status = %q, want %q", got.Status, "pending")
	}
}
