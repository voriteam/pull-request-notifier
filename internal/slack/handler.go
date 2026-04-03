package slack

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/voriteam/pull-request-notifier/internal/db"
	"github.com/voriteam/pull-request-notifier/internal/github"
)

// Handler handles Slack slash commands and block interactivity.
type Handler struct {
	signingSecret      string
	store              *db.Store
	github             *github.Client
	slack              *Client
	oauthBaseURL       string
	githubClientID     string
	githubClientSecret string
}

// NewHandler creates a new Slack interaction handler.
func NewHandler(signingSecret string, store *db.Store, githubClient *github.Client, slackClient *Client, oauthBaseURL, githubClientID, githubClientSecret string) *Handler {
	return &Handler{
		signingSecret:      signingSecret,
		store:              store,
		github:             githubClient,
		slack:              slackClient,
		oauthBaseURL:       oauthBaseURL,
		githubClientID:     githubClientID,
		githubClientSecret: githubClientSecret,
	}
}

// HandleCommand handles POST /slack/commands (slash commands).
func (h *Handler) HandleCommand(w http.ResponseWriter, r *http.Request) {
	body, err := h.verifyAndRead(r)
	if err != nil {
		slog.Error("slack command: verification failed", "err", err)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	vals, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	command := vals.Get("command")
	slackUserID := vals.Get("user_id")

	switch command {
	case "/link-github":
		h.handleLinkGitHub(w, slackUserID)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func (h *Handler) handleLinkGitHub(w http.ResponseWriter, slackUserID string) {
	// Generate a random state token.
	state := randomHex(16)
	if err := h.store.SaveOAuthState(state, slackUserID); err != nil {
		slog.Error("save oauth state", "err", err)
		jsonResponse(w, map[string]string{"text": "Something went wrong. Please try again."})
		return
	}

	oauthURL := fmt.Sprintf("%s/oauth/github?state=%s", h.oauthBaseURL, state)
	blocks := LinkGitHubMessage(oauthURL)

	// Respond ephemerally in Slack.
	jsonResponse(w, map[string]any{
		"response_type": "ephemeral",
		"blocks":        blocks,
		"text":          "Link your GitHub account",
	})
}

// HandleEvent handles POST /slack/events (Events API).
func (h *Handler) HandleEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// Slack sends a url_verification challenge on first setup.
	var envelope struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	if envelope.Type == "url_verification" {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(envelope.Challenge))
		return
	}

	// For all other events, verify the signature.
	// Re-create the request body for verification since we already read it.
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	if _, err := h.verifyAndRead(r); err != nil {
		slog.Error("slack event: verification failed", "err", err)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	w.WriteHeader(http.StatusOK)

	var event struct {
		Event struct {
			Type string `json:"type"`
			User string `json:"user"`
		} `json:"event"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		return
	}

	// No bot events currently handled.
}

// HandleInteraction handles POST /slack/interactions (button clicks, modal submissions).
func (h *Handler) HandleInteraction(w http.ResponseWriter, r *http.Request) {
	body, err := h.verifyAndRead(r)
	if err != nil {
		slog.Error("slack interaction: verification failed", "err", err)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Slack sends interactions as form-encoded with a "payload" field containing JSON.
	vals, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	payloadStr := vals.Get("payload")

	var payload interactionPayload
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	switch payload.Type {
	case "block_actions":
		h.handleBlockActions(w, &payload)
	case "view_submission":
		h.handleViewSubmission(w, &payload)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func (h *Handler) handleBlockActions(w http.ResponseWriter, payload *interactionPayload) {
	w.WriteHeader(http.StatusOK) // Acknowledge immediately.

	for _, action := range payload.Actions {
		switch {
		case action.ActionID == "reply":
			h.handleReplyButton(payload.TriggerID, action.BlockID)

		case strings.HasPrefix(action.ActionID, "react:"):
			reaction := strings.TrimPrefix(action.ActionID, "react:")
			h.handleReaction(payload.User.ID, action.BlockID, reaction)

		case action.ActionID == "react_overflow":
			h.handleReaction(payload.User.ID, action.BlockID, action.SelectedOption.Value)

		}
	}
}

func (h *Handler) handleReplyButton(triggerID, blockID string) {
	ctx, err := DecodeCommentContext(blockID)
	if err != nil {
		slog.Error("decode comment context", "blockID", blockID, "err", err)
		return
	}
	modal := ReplyModal(ctx)
	if err := h.slack.OpenModal(context.Background(), triggerID, modal); err != nil {
		slog.Error("open reply modal", "err", err)
	}
}

func (h *Handler) handleReaction(slackUserID, blockID, reaction string) {
	ctx, err := DecodeCommentContext(blockID)
	if err != nil {
		slog.Error("decode comment context for reaction", "err", err)
		return
	}

	mapping, err := h.store.GetMappingBySlackUserID(slackUserID)
	if err != nil || mapping == nil {
		slog.Warn("no github mapping for slack user", "slack_user_id", slackUserID)
		return
	}

	bgCtx := context.Background()
	err = h.github.AddReaction(bgCtx, mapping.GitHubToken, ctx.Repo, ctx.CommentID, ctx.CommentType, reaction)
	if err != nil && errors.Is(err, github.ErrUnauthorized) {
		newToken, refreshErr := h.refreshUserToken(bgCtx, mapping)
		if refreshErr != nil {
			slog.Error("refresh token for reaction", "err", refreshErr)
			return
		}
		err = h.github.AddReaction(bgCtx, newToken, ctx.Repo, ctx.CommentID, ctx.CommentType, reaction)
	}
	if err != nil {
		slog.Error("add github reaction", "err", err)
	}
}

func (h *Handler) handleViewSubmission(w http.ResponseWriter, payload *interactionPayload) {
	if payload.View.CallbackID != "reply_modal" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Extract the reply text from the modal input.
	replyText := payload.View.State.Values["reply_block"]["reply_text"].Value
	if strings.TrimSpace(replyText) == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	ctx, err := DecodeCommentContext(payload.View.PrivateMetadata)
	if err != nil {
		slog.Error("decode modal context", "err", err)
		w.WriteHeader(http.StatusOK)
		return
	}

	mapping, err := h.store.GetMappingBySlackUserID(payload.User.ID)
	if err != nil || mapping == nil {
		slog.Warn("no github mapping for slack user", "slack_user_id", payload.User.ID)
		// Return a modal error to the user.
		jsonResponse(w, map[string]any{
			"response_action": "errors",
			"errors": map[string]string{
				"reply_block": "Your GitHub account is not linked. Run /link-github first.",
			},
		})
		return
	}

	w.WriteHeader(http.StatusOK) // Close the modal immediately.

	go func() {
		bgCtx := context.Background()
		err := h.github.PostReply(bgCtx, mapping.GitHubToken, ctx.Repo, ctx.PRNumber, ctx.CommentID, ctx.CommentType, replyText)
		if err != nil && errors.Is(err, github.ErrUnauthorized) {
			newToken, refreshErr := h.refreshUserToken(bgCtx, mapping)
			if refreshErr != nil {
				slog.Error("refresh token for reply", "err", refreshErr)
				return
			}
			err = h.github.PostReply(bgCtx, newToken, ctx.Repo, ctx.PRNumber, ctx.CommentID, ctx.CommentType, replyText)
		}
		if err != nil {
			slog.Error("post github reply", "err", err)
		}
	}()
}

// refreshUserToken uses the stored refresh token to obtain a new access token,
// updates the DB, and returns the new access token. If the refresh fails or no
// refresh token is stored, it DMs the user asking them to re-link.
func (h *Handler) refreshUserToken(ctx context.Context, mapping *db.UserMapping) (string, error) {
	if mapping.RefreshToken == "" {
		h.notifyRelinkNeeded(ctx, mapping)
		return "", fmt.Errorf("no refresh token stored for user %s", mapping.GitHubUsername)
	}

	oauthToken, err := h.github.RefreshToken(ctx, h.githubClientID, h.githubClientSecret, mapping.RefreshToken)
	if err != nil {
		h.notifyRelinkNeeded(ctx, mapping)
		return "", fmt.Errorf("refresh github token: %w", err)
	}

	var tokenExpiresAt *time.Time
	if oauthToken.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(oauthToken.ExpiresIn) * time.Second)
		tokenExpiresAt = &t
	}

	if err := h.store.UpsertUserMapping(mapping.GitHubUsername, mapping.SlackUserID, oauthToken.AccessToken, oauthToken.RefreshToken, tokenExpiresAt); err != nil {
		return "", fmt.Errorf("update refreshed token: %w", err)
	}

	slog.Info("refreshed github token", "github_username", mapping.GitHubUsername)
	return oauthToken.AccessToken, nil
}

// notifyRelinkNeeded sends a Slack DM asking the user to re-link their GitHub account.
func (h *Handler) notifyRelinkNeeded(ctx context.Context, mapping *db.UserMapping) {
	text := "Your GitHub token has expired. Please run `/link-github` to re-authorize."
	blocks := []Block{sectionBlock(text)}
	if _, err := h.slack.PostDM(ctx, mapping.SlackUserID, blocks, text); err != nil {
		slog.Error("notify relink needed", "err", err, "slack_user_id", mapping.SlackUserID)
	}
}

// --- Signature verification ---

func (h *Handler) verifyAndRead(r *http.Request) ([]byte, error) {
	timestamp := r.Header.Get("X-Slack-Request-Timestamp")
	signature := r.Header.Get("X-Slack-Signature")

	// Reject requests older than 5 minutes to prevent replay attacks.
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || abs(time.Now().Unix()-ts) > 300 {
		return nil, fmt.Errorf("invalid or stale timestamp")
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	sigBase := "v0:" + timestamp + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(h.signingSecret))
	mac.Write([]byte(sigBase))
	expected := "v0=" + fmt.Sprintf("%x", mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return nil, fmt.Errorf("signature mismatch")
	}
	return body, nil
}

// --- Interaction payload types ---

type interactionPayload struct {
	Type      string        `json:"type"`
	TriggerID string        `json:"trigger_id"`
	User      slackUser     `json:"user"`
	Actions   []slackAction `json:"actions"`
	View      slackView     `json:"view"`
}

type slackUser struct {
	ID string `json:"id"`
}

type slackAction struct {
	ActionID       string            `json:"action_id"`
	BlockID        string            `json:"block_id"`
	Value          string            `json:"value"`
	SelectedOption slackSelectOption `json:"selected_option"`
}

type slackSelectOption struct {
	Value string `json:"value"`
}

type slackView struct {
	CallbackID      string    `json:"callback_id"`
	PrivateMetadata string    `json:"private_metadata"`
	State           viewState `json:"state"`
}

type viewState struct {
	Values map[string]map[string]viewValue `json:"values"`
}

type viewValue struct {
	Value string `json:"value"`
}

// --- Helpers ---

func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return fmt.Sprintf("%x", b)
}
