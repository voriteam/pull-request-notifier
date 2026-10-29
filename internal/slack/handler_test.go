package slack

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voriteam/pull-request-notifier/internal/db"
)

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
		{"prefix-of-another-login still prefixes", "@alice2 hi", "alice", "@alice @alice2 hi"},
		{"trailing newline trimmed", "\n@alice hi", "alice", "\n@alice hi"},
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

func TestDeployNotificationsCommand(t *testing.T) {
	store, err := db.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	h := &Handler{store: store}

	run := func(user, arg string) string {
		w := httptest.NewRecorder()
		h.handleDeployNotifications(w, user, arg)
		var resp struct{ Text string }
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatal(err)
		}
		return resp.Text
	}

	if got := run("U1", "on"); !strings.Contains(got, "/link-github") {
		t.Errorf("unlinked user: got %q", got)
	}

	if err := store.UpsertUserMapping("octocat", "U1", "", "", nil); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ arg, want string }{
		{"status", "*off*"},
		{"on", "*on*"},
		{"", "*on*"},
		{"OFF", "*off*"},
		{"bogus", "Usage"},
	} {
		if got := run("U1", step.arg); !strings.Contains(got, step.want) {
			t.Errorf("%q: got %q, want %q", step.arg, got, step.want)
		}
	}

	if err := store.SetNotifyDeploys("U1", true); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertUserMapping("octocat", "U1", "new-token", "", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetDeployNotifySlackID("octocat"); got != "U1" {
		t.Errorf("opt-in lost on relink: got %q", got)
	}
}
