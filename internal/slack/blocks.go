package slack

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/voriteam/pull-request-notifier/internal/github"
)

// CommentContext is embedded in button block_ids and modal private_metadata
// so we can route interactions back to the right GitHub object.
type CommentContext struct {
	Repo        string `json:"r"`
	PRNumber    int    `json:"p"`
	CommentID   int64  `json:"c"`
	CommentType string `json:"t"` // "pr_comment" | "review_comment" | "review"
}

func (c CommentContext) Encode() string {
	b, _ := json.Marshal(c)
	return string(b)
}

func DecodeCommentContext(s string) (CommentContext, error) {
	var c CommentContext
	return c, json.Unmarshal([]byte(s), &c)
}

// block/element type constants matching Slack's Block Kit spec.
const (
	blockSection  = "section"
	blockContext  = "context"
	blockActions  = "actions"
	blockInput    = "input"
	textMrkdwn    = "mrkdwn"
	textPlainText = "plain_text"
)

// Block is a Slack Block Kit block.
type Block = map[string]any

type block = Block
type element map[string]any
type textObj map[string]any

func mrkdwn(text string) textObj {
	return textObj{"type": textMrkdwn, "text": text}
}

func plain(text string) textObj {
	return textObj{"type": textPlainText, "text": text, "emoji": true}
}

func sectionBlock(text string) block {
	return block{"type": blockSection, "text": mrkdwn(text)}
}

func contextBlock(text string) block {
	return block{"type": blockContext, "elements": []textObj{mrkdwn(text)}}
}

// ReviewRequestedBlocks builds the Block Kit payload for a review-requested DM.
func ReviewRequestedBlocks(authorLogin, prTitle, prURL string, filesChanged, additions, deletions int, status string, activity *github.PRActivity) []block {
	blocks := []block{
		sectionBlock(fmt.Sprintf("🔍 *%s* requested your review on:", authorLogin)),
		sectionBlock(fmt.Sprintf("*<%s|%s>*", prURL, prTitle)),
		contextBlock(fmt.Sprintf("%d files changed · +%d -%d · _%s_", filesChanged, additions, deletions, status)),
	}
	blocks = append(blocks, activityBlocks(activity)...)
	return blocks
}

// ReviewRequestedMergedBlocks builds the updated payload when a PR is merged.
func ReviewRequestedMergedBlocks(authorLogin, prTitle, prURL string, filesChanged, additions, deletions int, activity *github.PRActivity) []block {
	blocks := []block{
		sectionBlock(fmt.Sprintf("~🔍 *%s* requested your review on:~", authorLogin)),
		sectionBlock(fmt.Sprintf("~*<%s|%s>*~", prURL, prTitle)),
		contextBlock(fmt.Sprintf("~%d files changed · +%d -%d~ · ← Merged", filesChanged, additions, deletions)),
	}
	blocks = append(blocks, activityBlocks(activity)...)
	return blocks
}

// ReviewRequestedClosedBlocks builds the updated payload when a PR is closed without merging.
func ReviewRequestedClosedBlocks(authorLogin, prTitle, prURL string, filesChanged, additions, deletions int, activity *github.PRActivity) []block {
	blocks := []block{
		sectionBlock(fmt.Sprintf("~🔍 *%s* requested your review on:~", authorLogin)),
		sectionBlock(fmt.Sprintf("~*<%s|%s>*~", prURL, prTitle)),
		contextBlock(fmt.Sprintf("~%d files changed · +%d -%d~ · ✕ Closed", filesChanged, additions, deletions)),
	}
	blocks = append(blocks, activityBlocks(activity)...)
	return blocks
}

// activityBlocks builds context blocks showing approval status and comment counts.
func activityBlocks(activity *github.PRActivity) []block {
	if activity == nil {
		return nil
	}

	var blocks []block

	if len(activity.Approvals) > 0 {
		blocks = append(blocks, contextBlock(fmt.Sprintf("✅ %s approved", formatUserList(activity.Approvals))))
	}

	if activity.TotalComments > 0 {
		// Sort commenters by count descending.
		type kv struct {
			User  string
			Count int
		}
		var sorted []kv
		for u, c := range activity.Commenters {
			sorted = append(sorted, kv{u, c})
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].Count > sorted[j].Count })

		var names []string
		for _, s := range sorted {
			names = append(names, s.User)
		}

		blocks = append(blocks, contextBlock(fmt.Sprintf("💬 %d comments by %s", activity.TotalComments, formatUserList(names))))
	}

	return blocks
}

// formatUserList formats a list of usernames: "A", "A and B", "A, B, and N others".
func formatUserList(users []string) string {
	switch len(users) {
	case 0:
		return ""
	case 1:
		return users[0]
	case 2:
		return users[0] + " and " + users[1]
	case 3:
		return users[0] + ", " + users[1] + ", and " + users[2]
	default:
		return fmt.Sprintf("%s, %s, and %d others", users[0], users[1], len(users)-2)
	}
}

// ReviewSubmittedBlocks builds the payload for an approved or changes-requested review DM.
func ReviewSubmittedBlocks(reviewerLogin, prTitle, prURL, state, body string) []block {
	var icon, verb string
	switch state {
	case "approved":
		icon, verb = "✅", "approved"
	case "changes_requested":
		icon, verb = "❌", "requested changes on"
	default:
		icon, verb = "💬", "reviewed"
	}

	header := fmt.Sprintf("%s *%s* %s *<%s|%s>*", icon, reviewerLogin, verb, prURL, prTitle)
	blocks := []block{sectionBlock(header)}
	if body != "" {
		blocks = append(blocks, sectionBlock(fmt.Sprintf("> %s", truncate(body, 300))))
	}
	return blocks
}

// CommentBlocks builds the payload for a PR comment or review comment DM, with interactive buttons.
func CommentBlocks(commenterLogin, prTitle, commentURL string, commentBody string, ctx CommentContext) []block {
	header := fmt.Sprintf("*%s* commented on *<%s|%s>*:", commenterLogin, commentURL, prTitle)
	blockID := ctx.Encode()

	return []block{
		sectionBlock(header),
		sectionBlock(fmt.Sprintf("> %s", truncate(commentBody, 500))),
		{
			"type":     blockActions,
			"block_id": blockID,
			"elements": []element{
				{
					"type":      "button",
					"text":      plain("Reply"),
					"action_id": "reply",
					"style":     "primary",
				},
				{
					"type":      "button",
					"text":      plain("👍"),
					"action_id": "react:+1",
				},
				{
					"type":      "button",
					"text":      plain("👀"),
					"action_id": "react:eyes",
				},
				{
					"type":      "button",
					"text":      plain("🎉"),
					"action_id": "react:hooray",
				},
				{
					"type":      "overflow",
					"action_id": "react_overflow",
					"options": []element{
						{"text": plain("👎"), "value": "-1"},
						{"text": plain("😄"), "value": "laugh"},
						{"text": plain("😕"), "value": "confused"},
						{"text": plain("❤️"), "value": "heart"},
						{"text": plain("🚀"), "value": "rocket"},
					},
				},
			},
		},
	}
}

// ReplyModal builds the view payload for the reply modal.
func ReplyModal(ctx CommentContext) map[string]any {
	return map[string]any{
		"type":             "modal",
		"callback_id":      "reply_modal",
		"private_metadata": ctx.Encode(),
		"title":            plain("Reply"),
		"submit":           plain("Send"),
		"close":            plain("Cancel"),
		"blocks": []block{
			{
				"type":     blockInput,
				"block_id": "reply_block",
				"element": element{
					"type":        "plain_text_input",
					"action_id":   "reply_text",
					"multiline":   true,
					"placeholder": plain("Write a reply..."),
				},
				"label": plain("Reply"),
			},
		},
	}
}

// LinkGitHubMessage builds the DM sent when a user runs /link-github.
func LinkGitHubMessage(oauthURL string) []block {
	return []block{
		sectionBlock("To link your GitHub account, click the button below. This lets you reply to and react on PR comments directly from Slack."),
		{
			"type": blockActions,
			"elements": []element{
				{
					"type":      "button",
					"text":      plain("🔗 Link GitHub Account"),
					"action_id": "link_github",
					"url":       oauthURL,
					"style":     "primary",
				},
			},
		},
	}
}

// CheckRunFailedBlocks builds the payload for a failed CI check DM.
func CheckRunFailedBlocks(checkName, checkURL, repoName, branch string) []block {
	return []block{
		sectionBlock(fmt.Sprintf("❌ Check *<%s|%s>* failed", checkURL, checkName)),
		contextBlock(fmt.Sprintf("Repository: *%s*    Branch: *%s*", repoName, branch)),
	}
}

// truncate shortens a string and appends "…" if it exceeds max runes.
func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
