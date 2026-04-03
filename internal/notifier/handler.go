package notifier

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/voriteam/pull-request-notifier/internal/db"
	"github.com/voriteam/pull-request-notifier/internal/github"
	"github.com/voriteam/pull-request-notifier/internal/slack"
)

var tracer = otel.Tracer("pull-request-notifier/notifier")

// Handler processes GitHub webhook events and sends Slack DMs.
type Handler struct {
	webhookSecret     string
	enableBotComments bool
	store             *db.Store
	slack             *slack.Client
	github            *github.Client
}

// NewHandler creates a new GitHub webhook handler.
func NewHandler(webhookSecret string, enableBotComments bool, store *db.Store, slackClient *slack.Client, githubClient *github.Client) *Handler {
	return &Handler{
		webhookSecret:     webhookSecret,
		enableBotComments: enableBotComments,
		store:             store,
		slack:             slackClient,
		github:            githubClient,
	}
}

// HandleWebhook handles POST /webhooks/github.
func (h *Handler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	if !h.verifySignature(r.Header.Get("X-Hub-Signature-256"), body) {
		http.Error(w, "Invalid signature", http.StatusUnauthorized)
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	deliveryID := r.Header.Get("X-GitHub-Delivery")

	// Log the full webhook payload as structured JSON for Datadog faceting.
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		payload = string(body)
	}
	slog.Info("webhook.received",
		"github.event", event,
		"github.delivery_id", deliveryID,
		"github.payload", payload,
	)

	w.WriteHeader(http.StatusOK)

	spanAttrs := []attribute.KeyValue{
		attribute.String("github.event", event),
		attribute.String("github.delivery_id", deliveryID),
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err == nil {
		spanAttrs = append(spanAttrs, flattenJSON("github.payload", raw)...)
	}
	ctx, span := tracer.Start(context.Background(), "webhook."+event, trace.WithAttributes(spanAttrs...))

	go func() {
		defer span.End()
		switch event {
		case "pull_request":
			h.handlePullRequest(ctx, body)
		case "pull_request_review":
			h.handlePullRequestReview(ctx, body)
		case "pull_request_review_comment":
			h.handlePullRequestReviewComment(ctx, body)
		case "issue_comment":
			h.handleIssueComment(ctx, body)
		case "check_run":
			h.handleCheckRun(ctx, body)
		}
	}()
}

// flattenJSON recursively flattens a JSON map into dot-notation OTel attributes.
func flattenJSON(prefix string, m map[string]any) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	for k, v := range m {
		key := prefix + "." + k
		switch val := v.(type) {
		case map[string]any:
			attrs = append(attrs, flattenJSON(key, val)...)
		case []any:
			// Store arrays as JSON strings.
			if b, err := json.Marshal(val); err == nil {
				attrs = append(attrs, attribute.String(key, string(b)))
			}
		case string:
			attrs = append(attrs, attribute.String(key, val))
		case float64:
			if val == float64(int64(val)) {
				attrs = append(attrs, attribute.Int64(key, int64(val)))
			} else {
				attrs = append(attrs, attribute.Float64(key, val))
			}
		case bool:
			attrs = append(attrs, attribute.Bool(key, val))
		case nil:
			// Skip null values.
		default:
			attrs = append(attrs, attribute.String(key, fmt.Sprintf("%v", val)))
		}
	}
	return attrs
}

func (h *Handler) verifySignature(signature string, body []byte) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(h.webhookSecret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), sig)
}

// --- Event types ---

type pullRequestEvent struct {
	Action            string      `json:"action"`
	PullRequest       pullRequest `json:"pull_request"`
	Sender            ghUser      `json:"sender"`
	RequestedReviewer *ghUser     `json:"requested_reviewer"`
	Repository        ghRepo      `json:"repository"`
}

type pullRequest struct {
	Number       int    `json:"number"`
	Title        string `json:"title"`
	HTMLURL      string `json:"html_url"`
	State        string `json:"state"`
	Merged       bool   `json:"merged"`
	User         ghUser `json:"user"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	ChangedFiles int    `json:"changed_files"`
	Draft        bool   `json:"draft"`
}

type pullRequestReviewEvent struct {
	Action      string      `json:"action"`
	Review      review      `json:"review"`
	PullRequest pullRequest `json:"pull_request"`
	Repository  ghRepo      `json:"repository"`
}

type review struct {
	User  ghUser `json:"user"`
	State string `json:"state"`
	Body  string `json:"body"`
}

type pullRequestReviewCommentEvent struct {
	Action      string      `json:"action"`
	Comment     comment     `json:"comment"`
	PullRequest pullRequest `json:"pull_request"`
	Repository  ghRepo      `json:"repository"`
}

type issueCommentEvent struct {
	Action     string  `json:"action"`
	Comment    comment `json:"comment"`
	Issue      issue   `json:"issue"`
	Repository ghRepo  `json:"repository"`
}

type issue struct {
	Number      int      `json:"number"`
	Title       string   `json:"title"`
	HTMLURL     string   `json:"html_url"`
	User        ghUser   `json:"user"`
	PullRequest *issuePR `json:"pull_request"`
}

type issuePR struct {
	URL string `json:"url"`
}

type comment struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	User    ghUser `json:"user"`
}

type checkRunEvent struct {
	Action     string   `json:"action"`
	CheckRun   checkRun `json:"check_run"`
	Repository ghRepo   `json:"repository"`
	Sender     ghUser   `json:"sender"`
}

type checkRun struct {
	Name         string       `json:"name"`
	Status       string       `json:"status"`
	Conclusion   string       `json:"conclusion"`
	HTMLURL      string       `json:"html_url"`
	PullRequests []checkRunPR `json:"pull_requests"`
	HeadSHA      string       `json:"head_sha"`
}

type checkRunPR struct {
	Number int   `json:"number"`
	Head   prRef `json:"head"`
}

type prRef struct {
	Ref string `json:"ref"`
}

type ghUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// isBot returns true if the GitHub user is a bot account.
func isBot(u ghUser) bool {
	return u.Type == "Bot" || strings.HasSuffix(u.Login, "[bot]")
}

type ghRepo struct {
	FullName string `json:"full_name"`
	Name     string `json:"name"`
}

// --- Event handlers ---

func (h *Handler) handlePullRequest(ctx context.Context, body []byte) {
	var evt pullRequestEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		slog.Error("parse pull_request event", "err", err)
		return
	}

	ctx, span := tracer.Start(ctx, "handlePullRequest", trace.WithAttributes(
		attribute.String("github.repository", evt.Repository.FullName),
		attribute.Int("github.pr.number", evt.PullRequest.Number),
		attribute.String("github.action", evt.Action),
	))
	defer span.End()

	// Track the PR author on any pull_request event so we can notify them on check failures.
	if err := h.store.SavePRAuthor(evt.Repository.FullName, evt.PullRequest.Number, evt.PullRequest.User.Login); err != nil {
		slog.Error("save pr author", "err", err)
	}

	switch evt.Action {
	case "review_requested":
		h.handleReviewRequested(ctx, &evt)
	case "closed":
		h.handlePRClosed(ctx, &evt)
	}
}

func (h *Handler) handleReviewRequested(ctx context.Context, evt *pullRequestEvent) {
	if evt.RequestedReviewer == nil {
		return
	}

	reviewer := evt.RequestedReviewer.Login
	slackUserID, err := h.store.GetMappingByGitHubUsername(reviewer)
	if err != nil {
		slog.Error("lookup reviewer mapping", "github", reviewer, "err", err)
		return
	}
	if slackUserID == "" {
		slog.Debug("no slack mapping for reviewer", "github", reviewer)
		return
	}

	pr := evt.PullRequest
	status := "open"
	if pr.Draft {
		status = "draft"
	}

	authorName := h.github.GetUserDisplayName(ctx, pr.User.Login)

	activity, err := h.github.GetPRActivity(ctx, evt.Repository.FullName, pr.Number, h.enableBotComments)
	if err != nil {
		slog.Error("fetch pr activity", "err", err)
	}

	blocks := slack.ReviewRequestedBlocks(
		authorName, pr.Title, pr.HTMLURL,
		pr.ChangedFiles, pr.Additions, pr.Deletions, status, activity,
	)
	fallback := fmt.Sprintf("%s requested your review on %s", authorName, pr.Title)

	ts, err := h.slack.PostDM(ctx, slackUserID, blocks, fallback)
	if err != nil {
		slog.Error("send review requested DM", "slack_user_id", slackUserID, "err", err)
		return
	}

	info := db.PRInfo{
		Author:       authorName,
		Title:        pr.Title,
		URL:          pr.HTMLURL,
		FilesChanged: pr.ChangedFiles,
		Additions:    pr.Additions,
		Deletions:    pr.Deletions,
		Draft:        pr.Draft,
	}
	if err := h.store.SavePRMessage(evt.Repository.FullName, pr.Number, slackUserID, ts, info); err != nil {
		slog.Error("save pr message", "err", err)
	}
}

func (h *Handler) handlePRClosed(ctx context.Context, evt *pullRequestEvent) {
	pr := evt.PullRequest
	repo := evt.Repository.FullName

	msgs, err := h.store.GetPRMessages(repo, pr.Number)
	if err != nil {
		slog.Error("get pr messages for close", "err", err)
		return
	}

	activity, err := h.github.GetPRActivity(ctx, repo, pr.Number, h.enableBotComments)
	if err != nil {
		slog.Error("fetch pr activity for close", "err", err)
	}

	for _, msg := range msgs {
		var blocks []slack.Block
		var fallback string

		if pr.Merged {
			blocks = slack.ReviewRequestedMergedBlocks(
				pr.User.Login, pr.Title, pr.HTMLURL,
				pr.ChangedFiles, pr.Additions, pr.Deletions, activity,
			)
			fallback = fmt.Sprintf("%s merged", pr.Title)
		} else {
			blocks = slack.ReviewRequestedClosedBlocks(
				pr.User.Login, pr.Title, pr.HTMLURL,
				pr.ChangedFiles, pr.Additions, pr.Deletions, activity,
			)
			fallback = fmt.Sprintf("%s closed", pr.Title)
		}

		// Slack DMs use the user ID as the channel for chat.update.
		if err := h.slack.UpdateDM(ctx, msg.SlackUserID, msg.MessageTS, blocks, fallback); err != nil {
			slog.Error("update pr message on close", "slack_user_id", msg.SlackUserID, "err", err)
		}
	}
}

func (h *Handler) handlePullRequestReview(ctx context.Context, body []byte) {
	var evt pullRequestReviewEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		slog.Error("parse pull_request_review event", "err", err)
		return
	}

	if evt.Action != "submitted" {
		return
	}

	// Refresh all existing review-requested DMs for this PR with updated activity.
	h.refreshPRMessages(ctx, evt.Repository.FullName, evt.PullRequest)

	// Only notify on approved, changes_requested, or commented (with body).
	state := evt.Review.State
	if state == "commented" && evt.Review.Body == "" {
		return
	}

	// Don't notify the PR author about their own review.
	prAuthor := evt.PullRequest.User.Login
	reviewer := evt.Review.User.Login
	if reviewer == prAuthor {
		return
	}

	slackUserID, err := h.store.GetMappingByGitHubUsername(prAuthor)
	if err != nil {
		slog.Error("lookup pr author mapping", "github", prAuthor, "err", err)
		return
	}
	if slackUserID == "" {
		slog.Debug("no slack mapping for pr author", "github", prAuthor)
		return
	}

	pr := evt.PullRequest
	reviewerName := h.github.GetUserDisplayName(ctx, reviewer)
	blocks := slack.ReviewSubmittedBlocks(reviewerName, pr.Title, pr.HTMLURL, state, evt.Review.Body)
	var action string
	switch state {
	case "approved":
		action = "approved"
	case "changes_requested":
		action = "requested changes for"
	default:
		action = "reviewed"
	}
	fallback := fmt.Sprintf("%s %s %s", reviewerName, action, pr.Title)

	if _, err := h.slack.PostDM(ctx, slackUserID, blocks, fallback); err != nil {
		slog.Error("send review submitted DM", "err", err)
	}
}

func (h *Handler) handlePullRequestReviewComment(ctx context.Context, body []byte) {
	var evt pullRequestReviewCommentEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		slog.Error("parse pull_request_review_comment event", "err", err)
		return
	}

	if evt.Action != "created" {
		return
	}

	pr := evt.PullRequest
	repo := evt.Repository.FullName

	// Refresh all existing review-requested DMs for this PR with updated activity.
	h.refreshPRMessages(ctx, repo, pr)

	// Don't notify the commenter about their own comment.
	prAuthor := pr.User.Login
	commenter := evt.Comment.User.Login
	if commenter == prAuthor {
		return
	}

	// Don't notify about bot comments (unless enabled).
	if !h.enableBotComments && isBot(evt.Comment.User) {
		return
	}

	slackUserID, err := h.store.GetMappingByGitHubUsername(prAuthor)
	if err != nil || slackUserID == "" {
		return
	}

	commentCtx := slack.CommentContext{
		Repo:        repo,
		PRNumber:    pr.Number,
		CommentID:   evt.Comment.ID,
		CommentType: "review_comment",
	}

	commenterName := h.github.GetUserDisplayName(ctx, commenter)
	blocks := slack.CommentBlocks(commenterName, pr.Title, pr.HTMLURL, evt.Comment.Body, commentCtx)
	fallback := fmt.Sprintf("%s commented on %s", commenterName, pr.Title)

	ts, err := h.slack.PostDM(ctx, slackUserID, blocks, fallback)
	if err != nil {
		slog.Error("send review comment DM", "err", err)
		return
	}

	if err := h.store.SaveCommentMessage(repo, pr.Number, evt.Comment.ID, "review_comment", slackUserID, ts); err != nil {
		slog.Error("save comment message", "err", err)
	}
}

func (h *Handler) handleIssueComment(ctx context.Context, body []byte) {
	var evt issueCommentEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		slog.Error("parse issue_comment event", "err", err)
		return
	}

	if evt.Action != "created" {
		return
	}

	// Only handle comments on PRs (issue_comment fires for both issues and PRs).
	if evt.Issue.PullRequest == nil {
		return
	}

	repo := evt.Repository.FullName

	// Refresh all existing review-requested DMs for this PR with updated activity.
	h.refreshPRMessages(ctx, repo, pullRequest{
		Number:  evt.Issue.Number,
		Title:   evt.Issue.Title,
		HTMLURL: evt.Issue.HTMLURL,
		User:    evt.Issue.User,
	})

	// Don't notify the commenter about their own comment.
	prAuthor := evt.Issue.User.Login
	commenter := evt.Comment.User.Login
	if commenter == prAuthor {
		return
	}

	// Don't notify about bot comments (unless enabled).
	if !h.enableBotComments && isBot(evt.Comment.User) {
		return
	}

	slackUserID, err := h.store.GetMappingByGitHubUsername(prAuthor)
	if err != nil || slackUserID == "" {
		return
	}

	commentCtx := slack.CommentContext{
		Repo:        repo,
		PRNumber:    evt.Issue.Number,
		CommentID:   evt.Comment.ID,
		CommentType: "pr_comment",
	}

	commenterName := h.github.GetUserDisplayName(ctx, commenter)
	blocks := slack.CommentBlocks(commenterName, evt.Issue.Title, evt.Issue.HTMLURL, evt.Comment.Body, commentCtx)
	fallback := fmt.Sprintf("%s commented on %s", commenterName, evt.Issue.Title)

	ts, err := h.slack.PostDM(ctx, slackUserID, blocks, fallback)
	if err != nil {
		slog.Error("send issue comment DM", "err", err)
		return
	}

	if err := h.store.SaveCommentMessage(repo, evt.Issue.Number, evt.Comment.ID, "pr_comment", slackUserID, ts); err != nil {
		slog.Error("save comment message", "err", err)
	}
}

func (h *Handler) handleCheckRun(ctx context.Context, body []byte) {
	var evt checkRunEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		slog.Error("check_run.parse_failed", "err", err)
		return
	}

	repo := evt.Repository.FullName
	log := slog.With(
		"github.repository", repo,
		"github.check_name", evt.CheckRun.Name,
		"github.action", evt.Action,
		"github.conclusion", evt.CheckRun.Conclusion,
		"github.head_sha", evt.CheckRun.HeadSHA,
	)

	if evt.Action != "completed" || evt.CheckRun.Conclusion != "failure" {
		log.Debug("check_run.skipped", "reason", "not a completed failure")
		return
	}

	if len(evt.CheckRun.PullRequests) == 0 {
		log.Warn("check_run.no_pull_requests", "reason", "check_run event had empty pull_requests array")
		return
	}

	for _, pr := range evt.CheckRun.PullRequests {
		prLog := log.With("github.pr_number", pr.Number, "github.branch", pr.Head.Ref)

		author, err := h.store.GetPRAuthor(repo, pr.Number)
		if err != nil {
			prLog.Error("check_run.get_pr_author_failed", "err", err)
			continue
		}
		if author == "" {
			author = evt.Sender.Login
			prLog.Info("check_run.pr_author_fallback", "github.sender", author)
		}
		if author == "" {
			prLog.Warn("check_run.no_author", "reason", "no stored author and no sender")
			continue
		}

		slackUserID, err := h.store.GetMappingByGitHubUsername(author)
		if err != nil {
			prLog.Error("check_run.lookup_mapping_failed", "github.author", author, "err", err)
			continue
		}
		if slackUserID == "" {
			prLog.Warn("check_run.no_slack_mapping", "github.author", author)
			continue
		}

		blocks := slack.CheckRunFailedBlocks(
			evt.CheckRun.Name, evt.CheckRun.HTMLURL,
			evt.Repository.Name, pr.Head.Ref,
		)
		fallback := fmt.Sprintf("Check %s failed", evt.CheckRun.Name)

		if _, err := h.slack.PostDM(ctx, slackUserID, blocks, fallback); err != nil {
			prLog.Error("check_run.send_dm_failed", "slack.user_id", slackUserID, "err", err)
		} else {
			prLog.Info("check_run.notified", "github.author", author, "slack.user_id", slackUserID)
		}
	}
}

// refreshPRMessages updates all existing review-requested DMs for a PR with current activity.
func (h *Handler) refreshPRMessages(ctx context.Context, repo string, pr pullRequest) {
	msgs, err := h.store.GetPRMessages(repo, pr.Number)
	if err != nil {
		slog.Error("get pr messages for refresh", "err", err)
		return
	}
	if len(msgs) == 0 {
		return
	}

	activity, err := h.github.GetPRActivity(ctx, repo, pr.Number, h.enableBotComments)
	if err != nil {
		slog.Error("fetch pr activity for refresh", "err", err)
		return
	}
	for _, msg := range msgs {
		status := "open"
		if msg.Draft {
			status = "draft"
		}

		blocks := slack.ReviewRequestedBlocks(
			msg.Author, msg.Title, msg.URL,
			msg.FilesChanged, msg.Additions, msg.Deletions, status, activity,
		)
		fallback := fmt.Sprintf("%s requested your review on %s", msg.Author, msg.Title)

		if err := h.slack.UpdateDM(ctx, msg.SlackUserID, msg.MessageTS, blocks, fallback); err != nil {
			slog.Error("update pr message with activity", "slack_user_id", msg.SlackUserID, "err", err)
		}
	}
}
