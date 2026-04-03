package config

import (
	"fmt"
	"os"
)

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	Port                 string
	DBPath               string
	BaseURL              string
	GitHubClientID       string
	GitHubClientSecret   string
	GitHubWebhookSecret  string
	GitHubAppID          string
	GitHubPrivateKey     string
	GitHubInstallationID string
	SlackBotToken        string
	SlackSigningSecret   string
}

// Load reads configuration from environment variables. Panics on missing required values.
func Load() *Config {
	return &Config{
		Port:                 getEnv("PORT", "8080"),
		DBPath:               getEnv("DB_PATH", "/data/pr-notifier.db"),
		BaseURL:              mustGetEnv("BASE_URL"),
		GitHubClientID:       mustGetEnv("GITHUB_CLIENT_ID"),
		GitHubClientSecret:   mustGetEnv("GITHUB_CLIENT_SECRET"),
		GitHubWebhookSecret:  mustGetEnv("GITHUB_WEBHOOK_SECRET"),
		GitHubAppID:          mustGetEnv("GITHUB_APP_ID"),
		GitHubPrivateKey:     mustGetEnv("GITHUB_PRIVATE_KEY"),
		GitHubInstallationID: mustGetEnv("GITHUB_INSTALLATION_ID"),
		SlackBotToken:        mustGetEnv("SLACK_BOT_TOKEN"),
		SlackSigningSecret:   mustGetEnv("SLACK_SIGNING_SECRET"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustGetEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required environment variable not set: %s", key))
	}
	return v
}
