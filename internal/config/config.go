package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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
	EnableBotComments    bool

	// Reminder scheduling.
	ReminderEnabled      bool
	ReminderHours        []int
	ReminderWeekdaysOnly bool
	ReminderDefaultTZ    string
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
		EnableBotComments:    getBoolEnv("ENABLE_BOT_COMMENTS", false),
		ReminderEnabled:      getBoolEnv("REMINDER_ENABLED", true),
		ReminderHours:        getIntListEnv("REMINDER_HOURS", []int{9, 13, 15}),
		ReminderWeekdaysOnly: getBoolEnv("REMINDER_WEEKDAYS_ONLY", true),
		ReminderDefaultTZ:    getEnv("REMINDER_DEFAULT_TZ", "America/Los_Angeles"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getBoolEnv(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return strings.EqualFold(v, "true") || v == "1"
}

// getIntListEnv parses a comma-separated list of integers (e.g. "9,13,15").
// Blank or unparseable entries are skipped; if nothing valid is found, the
// fallback is returned.
func getIntListEnv(key string, fallback []int) []int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	var out []int
	for _, part := range strings.Split(v, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}

func mustGetEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required environment variable not set: %s", key))
	}
	return v
}
