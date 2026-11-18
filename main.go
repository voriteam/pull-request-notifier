package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/voriteam/pull-request-notifier/internal/admin"
	"github.com/voriteam/pull-request-notifier/internal/config"
	"github.com/voriteam/pull-request-notifier/internal/db"
	"github.com/voriteam/pull-request-notifier/internal/github"
	"github.com/voriteam/pull-request-notifier/internal/notifier"
	"github.com/voriteam/pull-request-notifier/internal/oauth"
	"github.com/voriteam/pull-request-notifier/internal/reminder"
	"github.com/voriteam/pull-request-notifier/internal/slack"
	"github.com/voriteam/pull-request-notifier/internal/telemetry"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "sample-dm" {
		if err := runSampleDM(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "sample-dm:", err)
			os.Exit(1)
		}
		return
	}

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

	// Background work outlives the signal so it keeps running while in-flight webhooks drain.
	bgCtx, cancelBG := context.WithCancel(context.Background())
	defer cancelBG()
	go github.NewRedeliverer(ghClient, store).Run(bgCtx)

	if cfg.ReminderEnabled {
		defaultTZ, err := time.LoadLocation(cfg.ReminderDefaultTZ)
		if err != nil {
			slog.Error("invalid REMINDER_DEFAULT_TZ, falling back to UTC", "value", cfg.ReminderDefaultTZ, "err", err)
			defaultTZ = time.UTC
		}
		scheduler := reminder.New(store, ghClient, slackClient, cfg.ReminderHours, cfg.ReminderWeekdaysOnly, defaultTZ)
		go scheduler.Start(bgCtx)
	}

	adminHandler := admin.NewHandler(cfg.GitHubClientID, cfg.GitHubClientSecret, cfg.BaseURL, cfg.GitHubWebhookSecret, cfg.EnableBotComments, store, ghClient)
	oauthHandler := oauth.NewHandler(cfg.GitHubClientID, cfg.GitHubClientSecret, cfg.BaseURL, store, ghClient, slackClient, adminHandler)
	slackHandler := slack.NewHandler(cfg.SlackSigningSecret, store, ghClient, slackClient, cfg.BaseURL, cfg.GitHubClientID, cfg.GitHubClientSecret, github.TrackerOptions{
		ReviewBot:     cfg.ReviewBotLogin,
		IgnoredChecks: cfg.IgnoredChecks,
	})
	deployAnnouncer := notifier.NewDeployAnnouncer(cfg.DeployEnvironments, store, ghClient, slackClient)
	webhookHandler := notifier.NewHandler(cfg.GitHubWebhookSecret, cfg.EnableBotComments, store, slackClient, ghClient, deployAnnouncer)

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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: mux}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	slog.Info("starting server", "port", cfg.Port)

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server exited", "err", err)
			os.Exit(1)
		}
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	// Leaves headroom under the pod's 30s terminationGracePeriodSeconds.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown", "err", err)
	}
	webhookHandler.Wait()
	cancelBG()
	slog.Info("shutdown complete")
}
