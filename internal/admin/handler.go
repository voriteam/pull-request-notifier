package admin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/voriteam/pull-request-notifier/internal/db"
	"github.com/voriteam/pull-request-notifier/internal/github"
)

const sessionCookieName = "admin_session"

// Handler serves the admin UI with GitHub OAuth authentication.
type Handler struct {
	clientID     string
	clientSecret string
	baseURL      string
	signingKey   string // used to HMAC-sign session cookies
	store        *db.Store
	github       *github.Client
}

// NewHandler creates a new admin handler.
func NewHandler(clientID, clientSecret, baseURL, signingKey string, store *db.Store, githubClient *github.Client) *Handler {
	return &Handler{
		clientID:     clientID,
		clientSecret: clientSecret,
		baseURL:      baseURL,
		signingKey:   signingKey,
		store:        store,
		github:       githubClient,
	}
}

// HandleLinkedAccounts shows all GitHub↔Slack mappings. Requires a valid session.
func (h *Handler) HandleLinkedAccounts(w http.ResponseWriter, r *http.Request) {
	username := h.getSession(r)
	if username == "" {
		authURL := fmt.Sprintf(
			"https://github.com/login/oauth/authorize?client_id=%s&redirect_uri=%s&state=admin",
			h.clientID,
			h.baseURL+"/oauth/github/callback",
		)
		http.Redirect(w, r, authURL, http.StatusFound)
		return
	}

	mappings, err := h.store.ListAllMappings()
	if err != nil {
		slog.Error("list mappings", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
<title>CI Bot – Linked Accounts</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; max-width: 600px; margin: 40px auto; padding: 0 20px; color: #24292f; }
  h1 { font-size: 1.4em; }
  table { width: 100%%; border-collapse: collapse; margin-top: 16px; }
  th, td { text-align: left; padding: 8px 12px; border-bottom: 1px solid #d0d7de; }
  th { font-weight: 600; }
  .meta { color: #656d76; font-size: 0.85em; margin-top: 24px; }
</style>
</head>
<body>
<h1>Linked Accounts</h1>
<table>
<tr><th>GitHub Username</th><th>Slack User ID</th><th>Linked</th></tr>`)

	for _, m := range mappings {
		fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td><td>%s</td></tr>\n", htmlEscape(m.GitHubUsername), htmlEscape(m.SlackUserID), htmlEscape(m.CreatedAt))
	}

	fmt.Fprintf(w, `</table>
<p class="meta">Signed in as %s &middot; %d linked accounts</p>
</body>
</html>`, htmlEscape(username), len(mappings))
}

// CompleteLogin is called from the shared OAuth callback when the state is "admin".
// It exchanges the code, verifies org membership, sets a session cookie, and redirects.
func (h *Handler) CompleteLogin(w http.ResponseWriter, r *http.Request, code string) {
	token, err := h.github.ExchangeCode(r.Context(), h.clientID, h.clientSecret, code)
	if err != nil {
		slog.Error("admin oauth exchange", "err", err)
		http.Error(w, "GitHub authorization failed", http.StatusInternalServerError)
		return
	}

	username, err := h.github.GetAuthenticatedUser(r.Context(), token)
	if err != nil {
		slog.Error("admin get user", "err", err)
		http.Error(w, "Failed to fetch GitHub user", http.StatusInternalServerError)
		return
	}

	isMember, err := h.github.IsOrgMember(r.Context(), username)
	if err != nil {
		slog.Error("admin check org membership", "err", err)
		http.Error(w, "Failed to verify organization membership", http.StatusInternalServerError)
		return
	}
	if !isMember {
		slog.Warn("admin access denied: not an org member", "github_username", username)
		http.Error(w, "Access denied. You must be a member of the organization.", http.StatusForbidden)
		return
	}

	slog.Info("admin login", "github_username", username)
	h.setSession(w, username)
	http.Redirect(w, r, h.baseURL+"/admin", http.StatusFound)
}

func (h *Handler) setSession(w http.ResponseWriter, username string) {
	sig := h.sign(username)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    username + "." + sig,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400, // 24 hours
	})
}

func (h *Handler) getSession(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	parts := strings.SplitN(cookie.Value, ".", 2)
	if len(parts) != 2 {
		return ""
	}
	username, sig := parts[0], parts[1]
	if h.sign(username) != sig {
		return ""
	}
	return username
}

func (h *Handler) sign(data string) string {
	mac := hmac.New(sha256.New, []byte(h.signingKey))
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
