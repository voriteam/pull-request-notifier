package oauth

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/voriteam/pull-request-notifier/internal/admin"
	"github.com/voriteam/pull-request-notifier/internal/db"
	"github.com/voriteam/pull-request-notifier/internal/github"
	"github.com/voriteam/pull-request-notifier/internal/slack"
)

// Handler manages the GitHub OAuth flow for linking GitHub accounts to Slack users.
type Handler struct {
	clientID     string
	clientSecret string
	baseURL      string
	store        *db.Store
	github       *github.Client
	slack        *slack.Client
	admin        *admin.Handler
}

// NewHandler creates a new OAuth handler.
func NewHandler(clientID, clientSecret, baseURL string, store *db.Store, githubClient *github.Client, slackClient *slack.Client, adminHandler *admin.Handler) *Handler {
	return &Handler{
		clientID:     clientID,
		clientSecret: clientSecret,
		baseURL:      baseURL,
		store:        store,
		github:       githubClient,
		slack:        slackClient,
		admin:        adminHandler,
	}
}

// Start redirects the user to GitHub's OAuth authorization page.
// GET /oauth/github?state=<state>
func (h *Handler) Start(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		http.Error(w, "missing state parameter", http.StatusBadRequest)
		return
	}

	authURL := fmt.Sprintf(
		"https://github.com/login/oauth/authorize?client_id=%s&redirect_uri=%s&state=%s",
		h.clientID,
		h.baseURL+"/oauth/github/callback",
		state,
	)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// Callback handles the OAuth redirect from GitHub.
// GET /oauth/github/callback?code=<code>&state=<state>
func (h *Handler) Callback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	if code == "" || state == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}

	// Route admin login through the admin handler.
	if state == "admin" {
		h.admin.CompleteLogin(w, r, code)
		return
	}

	// Validate state and retrieve the associated Slack user ID.
	slackUserID, err := h.store.ConsumeOAuthState(state)
	if err != nil || slackUserID == "" {
		slog.Error("invalid or expired oauth state", "state", state, "err", err)
		http.Error(w, "Invalid or expired link. Please run /link-github again.", http.StatusBadRequest)
		return
	}

	// Exchange code for token.
	token, err := h.github.ExchangeCode(r.Context(), h.clientID, h.clientSecret, code)
	if err != nil {
		slog.Error("exchange github code", "err", err)
		http.Error(w, "Failed to complete GitHub authorization.", http.StatusInternalServerError)
		return
	}

	// Fetch the GitHub username.
	username, err := h.github.GetAuthenticatedUser(r.Context(), token)
	if err != nil {
		slog.Error("get github user", "err", err)
		http.Error(w, "Failed to fetch GitHub user info.", http.StatusInternalServerError)
		return
	}

	// Persist the mapping.
	if err := h.store.UpsertUserMapping(username, slackUserID, token); err != nil {
		slog.Error("upsert user mapping", "err", err)
		http.Error(w, "Failed to save account link.", http.StatusInternalServerError)
		return
	}

	slog.Info("linked github account", "github_username", username, "slack_user_id", slackUserID)

	// DM the user to confirm.
	_, _ = h.slack.PostDM(r.Context(), slackUserID,
		[]slack.Block{
			{"type": "section", "text": map[string]string{
				"type": "mrkdwn",
				"text": fmt.Sprintf("✅ Your GitHub account *%s* is now linked. You'll receive PR notifications here.", username),
			}},
		},
		fmt.Sprintf("GitHub account %s linked successfully.", username),
	)

	// Show a simple success page.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>GitHub Linked</title></head>
<body style="font-family:sans-serif;max-width:480px;margin:80px auto;text-align:center">
  <h2>✅ GitHub account linked!</h2>
  <p>Your GitHub account <strong>%s</strong> has been linked to your Slack account.</p>
  <p>You can close this window.</p>
</body>
</html>`, username)
}
