package notifier

import (
	"reflect"
	"strings"
	"testing"
)

func TestExtractMentions(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"empty", "", nil},
		{"no mentions", "just a plain comment", nil},
		{"single", "hey @alice please look", []string{"alice"}},
		{"start of string", "@alice take a look", []string{"alice"}},
		{"multiple with punctuation", "@bob, @carol and @dave!", []string{"bob", "carol", "dave"}},
		{"adjacent", "@bob @carol", []string{"bob", "carol"}},
		{"email rejected", "contact me at user@example.com", nil},
		{"hyphenated email rejected", "send to foo-@example.com please", nil},
		{"email with mention nearby", "ping @alice or user@example.com", []string{"alice"}},
		{"team mention skipped", "cc @vori/backend please", nil},
		{"team and user", "cc @vori/backend and @alice", []string{"alice"}},
		{"dedupe case insensitive preserves first casing", "@Alice @ALICE @alice.", []string{"Alice"}},
		{"leading hyphen rejected", "@-bad", nil},
		{"trailing hyphen rejected", "@bad- end", nil},
		{"consecutive hyphens rejected", "@a--b", nil},
		{"single hyphen ok", "see @a-b", []string{"a-b"}},
		{"parens", "(see @ev)", []string{"ev"}},
		{"newline boundary", "line one\n@alice line two", []string{"alice"}},
		{"end of string", "ping @alice", []string{"alice"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractMentions(tc.body)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("extractMentions(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestAddedMentions documents the new-mention diff that handleIssueComment
// applies on an "edited" action: mentions present in the new body but not in
// the pre-edit body should be the only ones notified.
func TestAddedMentions(t *testing.T) {
	cases := []struct {
		name    string
		oldBody string
		newBody string
		want    []string
	}{
		{"adds one", "hey @alice", "hey @alice and @bob", []string{"bob"}},
		{"no body change", "hey @alice", "hey @alice", nil},
		{"no new mentions", "hey @alice", "hey @alice please look", nil},
		{"first mention added", "looks good", "looks good @carol", []string{"carol"}},
		{"casing matches existing", "@Alice", "@Alice @alice @bob", []string{"bob"}},
		{"removed mention not notified", "@alice @bob", "@alice", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(map[string]bool)
			for _, login := range extractMentions(tc.oldBody) {
				seen[strings.ToLower(login)] = true
			}
			var got []string
			for _, login := range extractMentions(tc.newBody) {
				if seen[strings.ToLower(login)] {
					continue
				}
				got = append(got, login)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("added mentions(old=%q,new=%q) = %v, want %v", tc.oldBody, tc.newBody, got, tc.want)
			}
		})
	}
}
