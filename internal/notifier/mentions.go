package notifier

import (
	"regexp"
	"strings"
)

// mentionPattern matches a GitHub @-mention in a comment body.
// Group 1 is the username. The leading and trailing assertions reject
// matches inside email addresses (e.g., user@example.com) and team
// mentions (e.g., @org/team).
var mentionPattern = regexp.MustCompile(`(?:^|[^A-Za-z0-9])@([A-Za-z0-9](?:[A-Za-z0-9-]{0,38}))(?:[^A-Za-z0-9/-]|$)`)

// extractMentions returns deduplicated GitHub usernames @-mentioned in
// body, in first-seen order, preserving original casing. Dedup is
// case-insensitive since GitHub logins are. Team mentions (@org/team)
// and email-like @ are excluded. Logins with trailing hyphens or
// consecutive hyphens (which GitHub disallows) are dropped.
func extractMentions(body string) []string {
	// Scan with a sliding cursor advancing only past each captured username.
	// FindAllStringSubmatchIndex would consume the trailing boundary char and
	// drop adjacent mentions like "@bob @carol".
	seen := make(map[string]bool)
	var out []string
	for i := 0; i < len(body); {
		loc := mentionPattern.FindStringSubmatchIndex(body[i:])
		if loc == nil {
			break
		}
		login := body[i+loc[2] : i+loc[3]]
		i += loc[3]

		if strings.Contains(login, "--") || strings.HasSuffix(login, "-") {
			continue
		}
		key := strings.ToLower(login)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, login)
	}
	return out
}
