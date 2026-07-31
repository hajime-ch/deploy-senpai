package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/hajime-ch/deploy-senpai/internal/api"
	"github.com/hajime-ch/deploy-senpai/internal/cleanup"
	"github.com/hajime-ch/deploy-senpai/internal/config"
	"github.com/hajime-ch/deploy-senpai/internal/deployer"
)

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the deploy-senpai server",
		RunE:  runServe,
	}
	cmd.Flags().StringP("config", "c", "config.yaml", "Path to configuration file")
	return cmd
}

func runServe(cmd *cobra.Command, args []string) error {
	configPath, _ := cmd.Flags().GetString("config")

	// Setup logger
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Load configuration
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Update log level from config
	var level slog.Level
	switch cfg.Logging.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
	slog.SetDefault(logger)

	logger.Info("starting deploy-senpai",
		"domain", cfg.Domain.BaseDomain,
		"apps", len(cfg.Apps),
	)

	// The webhook endpoint is the one route outside the auth middleware, and its
	// only credential is the shared secret. Without it every webhook is refused,
	// so say so at startup rather than letting deploys silently stop working.
	if cfg.Server.WebhookSecret == "" {
		logger.Warn("server.webhook_secret is not set: all GitHub webhooks will be rejected. " +
			"Set it (e.g. via the WEBHOOK_SECRET environment variable) to enable webhook-driven deployments")
	}

	// Create deployer
	d := deployer.New(cfg, logger)

	// Create cleaner
	c := cleanup.New(cfg, d, logger)
	if err := c.Start(); err != nil {
		return fmt.Errorf("failed to start cleanup scheduler: %w", err)
	}
	defer c.Stop()

	// Create API server
	server := api.New(cfg, d, c, logger)
	defer server.Stop()

	// Setup HTTP server
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	httpServer := &http.Server{
		Addr:         addr,
		Handler:      server.Handler(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in goroutine
	errCh := make(chan error, 1)
	go func() {
		logger.Info("HTTP server starting", "addr", addr)
		if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
			errCh <- fmt.Errorf("HTTP server error: %w", err)
		}
	}()

	// Wait for shutdown signal or server error
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-quit:
		logger.Info("shutting down...")
	case err := <-errCh:
		return err
	}

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("server shutdown error: %w", err)
	}

	logger.Info("shutdown complete")
	return nil
}
