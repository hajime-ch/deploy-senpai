package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/hajime-ch/deploy-senpai/internal/auth"
	"github.com/hajime-ch/deploy-senpai/internal/cleanup"
	"github.com/hajime-ch/deploy-senpai/internal/config"
	"github.com/hajime-ch/deploy-senpai/internal/deployer"
	"github.com/hajime-ch/deploy-senpai/internal/github"
	"github.com/hajime-ch/deploy-senpai/internal/metrics"
	"github.com/hajime-ch/deploy-senpai/internal/ratelimit"
)

// Helper functions for health checks
var execCommandContext = exec.CommandContext
var writeFile = os.WriteFile
var removeFile = os.Remove

// Server handles HTTP requests
type Server struct {
	cfg         *config.Config
	deployer    *deployer.Deployer
	cleaner     *cleanup.Cleaner
	logger      *slog.Logger
	router      *chi.Mux
	auth        *auth.Authenticator
	rateLimiter *ratelimit.RateLimiter
	metrics     *metrics.Metrics
}

// New creates a new API server
func New(cfg *config.Config, d *deployer.Deployer, c *cleanup.Cleaner, logger *slog.Logger) *Server {
	s := &Server{
		cfg:      cfg,
		deployer: d,
		cleaner:  c,
		logger:   logger,
		router:   chi.NewRouter(),
		auth:     auth.New(cfg.Server.APIKeys, cfg.Server.EnableAuth),
		rateLimiter: ratelimit.New(ratelimit.Config{
			Enabled:           cfg.RateLimit.Enabled,
			RequestsPerMinute: cfg.RateLimit.RequestsPerMinute,
		}),
		metrics: metrics.New(),
	}

	s.setupRoutes()
	return s
}

// Metrics returns the server's metrics instance
func (s *Server) Metrics() *metrics.Metrics {
	return s.metrics
}

// Stop cleans up server resources
func (s *Server) Stop() {
	s.rateLimiter.Stop()
}

func (s *Server) setupRoutes() {
	r := s.router

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(s.securityHeadersMiddleware)
	r.Use(s.rateLimiter.Middleware)
	r.Use(s.metrics.Middleware)
	r.Use(s.loggingMiddleware)

	// Health check
	r.Get("/health", s.handleHealth)

	// Metrics endpoint (Prometheus format)
	r.Get("/metrics", s.metrics.Handler())

	// GitHub webhook endpoint
	r.Post("/webhook/github", s.handleGitHubWebhook)

	// API endpoints (protected by authentication)
	r.Route("/api/v1", func(r chi.Router) {
		// Apply authentication middleware to all API routes
		r.Use(s.auth.Middleware)

		// List all deployments
		r.Get("/deployments", s.handleListDeployments)

		// Get deployment status by ID
		r.Get("/deployments/status/{id}", s.handleGetDeploymentStatus)

		// Get specific deployment
		r.Get("/deployments/{app}/{branch}", s.handleGetDeployment)

		// Manual deploy trigger
		r.Post("/deployments/{app}/{branch}", s.handleDeploy)

		// Remove deployment
		r.Delete("/deployments/{app}/{branch}", s.handleRemove)

		// Trigger cleanup
		r.Post("/cleanup", s.handleCleanup)

		// List configured apps
		r.Get("/apps", s.handleListApps)
	})
}

// Handler returns the HTTP handler
func (s *Server) Handler() http.Handler {
	return s.router
}

func (s *Server) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prevent MIME type sniffing
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Prevent clickjacking
		w.Header().Set("X-Frame-Options", "DENY")
		// Strict Content Security Policy
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		// Referrer policy
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// Disable caching for API responses
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		w.Header().Set("Pragma", "no-cache")

		next.ServeHTTP(w, r)
	})
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		defer func() {
			s.logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"duration", time.Since(start),
				"remote", r.RemoteAddr,
			)
		}()

		next.ServeHTTP(ww, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	health := s.checkHealth(r.Context())

	w.Header().Set("Content-Type", "application/json")

	switch health.Status {
	case "unhealthy":
		w.WriteHeader(http.StatusServiceUnavailable)
	case "degraded":
		w.WriteHeader(http.StatusOK) // Still return 200 for degraded
	}

	_ = json.NewEncoder(w).Encode(health)
}

type healthCheck struct {
	Status     string                 `json:"status"`
	Time       string                 `json:"time"`
	Components map[string]interface{} `json:"components"`
}

func (s *Server) checkHealth(ctx context.Context) healthCheck {
	health := healthCheck{
		Status:     "ok",
		Time:       time.Now().Format(time.RFC3339),
		Components: make(map[string]interface{}),
	}

	// Check Docker
	dockerStatus := s.checkDocker(ctx)
	health.Components["docker"] = dockerStatus
	if dockerStatus["status"] != "ok" {
		health.Status = "degraded"
	}

	// Check storage
	storageStatus := s.checkStorage()
	health.Components["storage"] = storageStatus
	if storageStatus["status"] != "ok" {
		health.Status = "degraded"
	}

	// Add deployment count
	deployments := s.deployer.List()
	health.Components["deployments"] = map[string]interface{}{
		"status": "ok",
		"count":  len(deployments),
	}

	return health
}

func (s *Server) checkDocker(ctx context.Context) map[string]interface{} {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Try to run a simple docker command
	cmd := execCommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}")
	output, err := cmd.Output()
	if err != nil {
		return map[string]interface{}{
			"status": "error",
			"error":  "docker not accessible",
		}
	}

	return map[string]interface{}{
		"status":  "ok",
		"version": strings.TrimSpace(string(output)),
	}
}

func (s *Server) checkStorage() map[string]interface{} {
	dataDir := s.cfg.Storage.DataDir

	// Check if data directory is accessible and writable
	testFile := dataDir + "/.health_check"
	if err := writeFile(testFile, []byte("ok"), 0644); err != nil {
		return map[string]interface{}{
			"status": "error",
			"error":  "storage not writable",
		}
	}
	_ = removeFile(testFile)

	return map[string]interface{}{
		"status": "ok",
		"path":   dataDir,
	}
}

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	s.metrics.IncWebhooksReceived()

	payload, err := github.ParseWebhook(r, s.cfg.Server.WebhookSecret)
	if err != nil {
		s.logger.Error("failed to parse webhook", "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.logger.Info("received webhook",
		"event", payload.Event,
		"repo", payload.Repository,
		"branch", payload.Branch,
		"delivery", payload.Delivery,
	)

	switch payload.Event {
	case github.EventPing:
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "pong"})
		return

	case github.EventPush:
		if payload.Deleted {
			// Branch was deleted
			if s.cfg.Cleanup.OnBranchDelete {
				appName, _, found := s.cfg.GetAppByRepo(payload.Repository)
				if found {
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
						defer cancel()
						_ = s.cleaner.CleanupBranch(ctx, appName, payload.Branch)
					}()
				}
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "branch deletion noted"})
			return
		}

		// Find the app config
		appName, _, found := s.cfg.GetAppByRepo(payload.Repository)
		if !found {
			s.logger.Warn("unknown repository", "repo", payload.Repository)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "repository not configured"})
			return
		}

		// Use commit SHA as the image tag (or branch name as fallback)
		imageTag := payload.CommitSHA
		if payload.IsTag {
			// For tag pushes, use the full tag name (e.g. "v1.2.3") as the image tag
			imageTag = payload.Branch
		} else if len(imageTag) > 7 {
			imageTag = imageTag[:7] // Short SHA
		}
		if imageTag == "" {
			imageTag = config.SanitizeBranchName(payload.Branch)
		}

		// Create pending deployment to get ID
		dep, err := s.deployer.StartDeployment(appName, payload.Branch, imageTag)
		if err != nil {
			s.logger.Error("failed to start deployment", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Trigger deployment asynchronously
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()

			s.metrics.IncDeploymentsTotal()

			_, err := s.deployer.Deploy(ctx, appName, payload.Branch, imageTag)
			if err != nil {
				s.metrics.IncDeploymentsFailed()
				s.logger.Error("deployment failed",
					"id", dep.ID,
					"app", appName,
					"branch", payload.Branch,
					"error", err,
				)
			} else {
				s.metrics.IncDeploymentsSucceeded()
			}

			// Update active deployments count
			s.metrics.SetActiveDeployments(int64(len(s.deployer.List())))
		}()

		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"message":       "deployment triggered",
			"deployment_id": dep.ID,
			"app":           appName,
			"branch":        payload.Branch,
		})

	case github.EventDelete:
		if payload.Deleted && s.cfg.Cleanup.OnBranchDelete {
			appName, _, found := s.cfg.GetAppByRepo(payload.Repository)
			if found {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()
					_ = s.cleaner.CleanupBranch(ctx, appName, payload.Branch)
				}()
			}
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "delete event processed"})

	default:
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "event ignored"})
	}
}

func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	deployments := s.deployer.List()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(deployments)
}

func (s *Server) handleGetDeployment(w http.ResponseWriter, r *http.Request) {
	app := chi.URLParam(r, "app")
	branch := chi.URLParam(r, "branch")

	dep, found := s.deployer.Get(app, branch)
	if !found {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(dep)
}

func (s *Server) handleGetDeploymentStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	dep, found := s.deployer.GetByID(id)
	if !found {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id":            dep.ID,
		"app":           dep.App,
		"branch":        dep.Branch,
		"status":        dep.Status,
		"url":           dep.URL,
		"error_message": dep.ErrorMessage,
		"created_at":    dep.CreatedAt,
		"updated_at":    dep.UpdatedAt,
	})
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	app := chi.URLParam(r, "app")
	branch := chi.URLParam(r, "branch")

	// Check if app exists
	_, ok := s.cfg.Apps[app]
	if !ok {
		http.Error(w, "unknown app", http.StatusNotFound)
		return
	}

	// Parse optional image tag from body
	var body struct {
		ImageTag string `json:"image_tag"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	imageTag := body.ImageTag
	if imageTag == "" {
		imageTag = config.SanitizeBranchName(branch)
	}

	// Trigger deployment
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	dep, err := s.deployer.Deploy(ctx, app, branch, imageTag)
	if err != nil {
		s.logger.Error("deployment failed", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(dep)
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	app := chi.URLParam(r, "app")
	branch := chi.URLParam(r, "branch")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	if err := s.deployer.Remove(ctx, app, branch); err != nil {
		s.logger.Error("removal failed", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCleanup(w http.ResponseWriter, r *http.Request) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		s.cleaner.Run(ctx)
	}()

	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": "cleanup triggered"})
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	type appInfo struct {
		Name            string `json:"name"`
		Repo            string `json:"repo"`
		ComposeTemplate string `json:"compose_template"`
	}

	apps := make([]appInfo, 0, len(s.cfg.Apps))
	for name, app := range s.cfg.Apps {
		apps = append(apps, appInfo{
			Name:            name,
			Repo:            app.Repo,
			ComposeTemplate: app.ComposeTemplate,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(apps)
}
