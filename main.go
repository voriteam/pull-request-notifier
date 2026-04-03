package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/voriteam/pull-request-notifier/internal/admin"
	"github.com/voriteam/pull-request-notifier/internal/config"
	"github.com/voriteam/pull-request-notifier/internal/db"
	"github.com/voriteam/pull-request-notifier/internal/github"
	"github.com/voriteam/pull-request-notifier/internal/notifier"
	"github.com/voriteam/pull-request-notifier/internal/oauth"
	"github.com/voriteam/pull-request-notifier/internal/slack"
	"github.com/voriteam/pull-request-notifier/internal/telemetry"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.Info("starting", "version", os.Getenv("VERSION"))

	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "pull-request-notifier"
	}
	shutdown := telemetry.Init(context.Background(), serviceName, os.Getenv("VERSION"))
	defer shutdown(context.Background())

	cfg := config.Load()

	store, err := db.New(cfg.DBPath)
	if err != nil {
		slog.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	ghClient, err := github.NewClient(cfg.GitHubAppID, cfg.GitHubPrivateKey, cfg.GitHubInstallationID)
	if err != nil {
		slog.Error("failed to create github client", "err", err)
		os.Exit(1)
	}
	slackClient := slack.NewClient(cfg.SlackBotToken)

	adminHandler := admin.NewHandler(cfg.GitHubClientID, cfg.GitHubClientSecret, cfg.BaseURL, cfg.GitHubWebhookSecret, cfg.EnableBotComments, store, ghClient)
	oauthHandler := oauth.NewHandler(cfg.GitHubClientID, cfg.GitHubClientSecret, cfg.BaseURL, store, ghClient, slackClient, adminHandler)
	slackHandler := slack.NewHandler(cfg.SlackSigningSecret, store, ghClient, slackClient, cfg.BaseURL, cfg.GitHubClientID, cfg.GitHubClientSecret)
	webhookHandler := notifier.NewHandler(cfg.GitHubWebhookSecret, cfg.EnableBotComments, store, slackClient, ghClient)

	mux := http.NewServeMux()

	// GitHub webhook endpoint.
	mux.HandleFunc("POST /webhooks/github", webhookHandler.HandleWebhook)

	// Slack endpoints.
	mux.HandleFunc("POST /slack/commands", slackHandler.HandleCommand)
	mux.HandleFunc("POST /slack/events", slackHandler.HandleEvent)
	mux.HandleFunc("POST /slack/interactions", slackHandler.HandleInteraction)

	// GitHub OAuth flow.
	mux.HandleFunc("GET /oauth/github", oauthHandler.Start)
	mux.HandleFunc("GET /oauth/github/callback", oauthHandler.Callback)

	// Admin UI.
	mux.HandleFunc("GET /admin", adminHandler.HandleLinkedAccounts)

	// Health check.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(`{"status":"ok","version":"%s"}`, os.Getenv("VERSION"))))
	})

	slog.Info("starting server", "port", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, mux); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}
