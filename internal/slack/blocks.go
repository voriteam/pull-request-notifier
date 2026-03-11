package slack

import (
	"encoding/json"
	"fmt"
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
	blockSection = "section"
	blockContext = "context"
	blockActions = "actions"
	blockInput   = "input"
	blockDivider = "divider"

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

func dividerBlock() block {
	return block{"type": blockDivider}
}

// ReviewRequestedBlocks builds the Block Kit payload for a review-requested DM.
func ReviewRequestedBlocks(authorLogin, prTitle, prURL string, filesChanged, additions, deletions int, status string) []block {
	return []block{
		sectionBlock(fmt.Sprintf("🔍 *%s* requested your review on:", authorLogin)),
		sectionBlock(fmt.Sprintf("*<%s|%s>*", prURL, prTitle)),
		contextBlock(fmt.Sprintf("%d files changed · +%d -%d · _%s_", filesChanged, additions, deletions, status)),
	}
}

// ReviewRequestedMergedBlocks builds the updated payload when a PR is merged.
func ReviewRequestedMergedBlocks(authorLogin, prTitle, prURL string, filesChanged, additions, deletions int) []block {
	return []block{
		sectionBlock(fmt.Sprintf("~🔍 *%s* requested your review on:~", authorLogin)),
		sectionBlock(fmt.Sprintf("~*<%s|%s>*~", prURL, prTitle)),
		contextBlock(fmt.Sprintf("~%d files changed · +%d -%d~ · ← Merged", filesChanged, additions, deletions)),
	}
}

// ReviewRequestedClosedBlocks builds the updated payload when a PR is closed without merging.
func ReviewRequestedClosedBlocks(authorLogin, prTitle, prURL string, filesChanged, additions, deletions int) []block {
	return []block{
		sectionBlock(fmt.Sprintf("~🔍 *%s* requested your review on:~", authorLogin)),
		sectionBlock(fmt.Sprintf("~*<%s|%s>*~", prURL, prTitle)),
		contextBlock(fmt.Sprintf("~%d files changed · +%d -%d~ · ✕ Closed", filesChanged, additions, deletions)),
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
func CommentBlocks(commenterLogin, prTitle, prURL string, commentBody string, ctx CommentContext) []block {
	header := fmt.Sprintf("*%s* commented on *<%s|%s>*:", commenterLogin, prURL, prTitle)
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
