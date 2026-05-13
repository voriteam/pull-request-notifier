package slack

import "testing"

func TestPrefixMention(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		commenter string
		want      string
	}{
		{"empty commenter is no-op", "hello", "", "hello"},
		{"prefixes simple body", "hello", "alice", "@alice hello"},
		{"already mentioned exactly", "@alice hi", "alice", "@alice hi"},
		{"already mentioned different case", "@Alice hi", "alice", "@Alice hi"},
		{"leading whitespace counts as already mentioned", "  @alice hi", "alice", "  @alice hi"},
		{"prefix when mention appears later", "hi @alice", "alice", "@alice hi @alice"},
		{"different user already mentioned still prefixes", "@bob hi", "alice", "@alice @bob hi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := prefixMention(tc.body, tc.commenter)
			if got != tc.want {
				t.Errorf("prefixMention(%q, %q) = %q, want %q", tc.body, tc.commenter, got, tc.want)
			}
		})
	}
}
