package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
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

	// GitHub login of a code review bot whose verdict gates "Ready to merge"
	// on the Home tab. Empty ignores bot reviews.
	ReviewBotLogin string

	// Checks whose names match are left out of the Home tab's CI state, for
	// checks that wait on a person rather than a build. Nil keeps every check.
	IgnoredChecks *regexp.Regexp

	// Reminder scheduling.
	ReminderEnabled      bool
	ReminderHours        []int
	ReminderWeekdaysOnly bool
	ReminderDefaultTZ    string

	// GitHub deployment environments whose successful deployments are announced.
	// Empty turns deploy notifications off.
	DeployEnvironments []string
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
		ReviewBotLogin:       os.Getenv("REVIEW_BOT_LOGIN"),
		IgnoredChecks:        mustGetRegexpEnv("IGNORED_CHECKS"),
		ReminderEnabled:      getBoolEnv("REMINDER_ENABLED", true),
		ReminderHours:        getIntListEnv("REMINDER_HOURS", []int{9, 13, 15}),
		ReminderWeekdaysOnly: getBoolEnv("REMINDER_WEEKDAYS_ONLY", true),
		ReminderDefaultTZ:    getEnv("REMINDER_DEFAULT_TZ", "America/Los_Angeles"),
		DeployEnvironments:   mustGetJSONStringListEnv("DEPLOY_ENVIRONMENTS"),
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

// mustGetJSONStringListEnv parses a JSON array of strings. Unset is an empty list.
func mustGetJSONStringListEnv(key string) []string {
	list, err := parseJSONStringList(os.Getenv(key))
	if err != nil {
		panic(fmt.Sprintf("invalid %s: %v", key, err))
	}
	return list
}

func parseJSONStringList(v string) ([]string, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	var list []string
	err := json.Unmarshal([]byte(v), &list)
	return list, err
}

// mustGetRegexpEnv compiles a Go regexp. Unset is nil.
func mustGetRegexpEnv(key string) *regexp.Regexp {
	re, err := parseRegexp(os.Getenv(key))
	if err != nil {
		panic(fmt.Sprintf("invalid %s: %v", key, err))
	}
	return re
}

func parseRegexp(v string) (*regexp.Regexp, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	return regexp.Compile(v)
}

func mustGetEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required environment variable not set: %s", key))
	}
	return v
}
