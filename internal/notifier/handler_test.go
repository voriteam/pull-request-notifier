package notifier

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/voriteam/pull-request-notifier/internal/db"
	"github.com/voriteam/pull-request-notifier/internal/github"
	"github.com/voriteam/pull-request-notifier/internal/slack"
)

func (f *fakeDMs) UpdateDM(context.Context, string, string, []slack.Block, string) error {
	return nil
}

// fakeProfiles stands in for the GitHub client: display names are logins and
// PRs have no activity.
type fakeProfiles struct{}

func (fakeProfiles) GetPRActivity(context.Context, string, int, bool) (*github.PRActivity, error) {
	return &github.PRActivity{Commenters: map[string]int{}}, nil
}

func (fakeProfiles) GetUserDisplayName(_ context.Context, login string) string { return login }

func newTestHandler(t *testing.T, enableBotComments bool) (*Handler, *fakeDMs) {
	t.Helper()
	store, err := db.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for _, u := range []struct{ login, slack string }{{"alice", "UA"}, {"bob", "UB"}, {"carol", "UC"}} {
		if err := store.UpsertUserMapping(u.login, u.slack, "", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	dms := &fakeDMs{}
	return &Handler{enableBotComments: enableBotComments, store: store, slack: dms, github: fakeProfiles{}}, dms
}

// issueComment builds an issue_comment event on PR #7, authored by carol.
func issueComment(t *testing.T, action string, commenter ghUser, body string) []byte {
	t.Helper()
	evt := issueCommentEvent{
		Action:     action,
		Comment:    comment{ID: 1, Body: body, HTMLURL: "https://github.com/voriteam/backend/pull/7#issuecomment-1", User: commenter},
		Issue:      issue{Number: 7, Title: "Add thing", HTMLURL: "https://github.com/voriteam/backend/pull/7", User: ghUser{Login: "carol", Type: "User"}, PullRequest: &issuePR{URL: "https://api.github.com/repos/voriteam/backend/pulls/7"}},
		Repository: ghRepo{FullName: backend, Name: "backend"},
	}
	b, err := json.Marshal(evt)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var (
	human = ghUser{Login: "bob", Type: "User"}
	bot   = ghUser{Login: "github-actions[bot]", Type: "Bot"}
)

func TestHandleIssueComment_BotMentionsAreDelivered(t *testing.T) {
	h, dms := newTestHandler(t, false)
	h.handleIssueComment(context.Background(), issueComment(t, "created", bot, "@alice your cluster is ready"))

	if got, want := users(dms.sent), []string{"UA"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DMs to %v, want %v (mentioned user only, not the PR author)", got, want)
	}
}

func TestHandleIssueComment_BotWithoutMentionsIsSilent(t *testing.T) {
	h, dms := newTestHandler(t, false)
	h.handleIssueComment(context.Background(), issueComment(t, "created", bot, "Build finished."))

	if len(dms.sent) != 0 {
		t.Errorf("DMs to %v, want none", users(dms.sent))
	}
}

func TestHandleIssueComment_BotCommentsEnabledNotifiesAuthor(t *testing.T) {
	h, dms := newTestHandler(t, true)
	h.handleIssueComment(context.Background(), issueComment(t, "created", bot, "@alice your cluster is ready"))

	if got, want := users(dms.sent), []string{"UC", "UA"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DMs to %v, want %v", got, want)
	}
}

func TestHandleIssueComment_HumanNotifiesAuthorAndMentions(t *testing.T) {
	h, dms := newTestHandler(t, false)
	h.handleIssueComment(context.Background(), issueComment(t, "created", human, "@alice can you look?"))

	if got, want := users(dms.sent), []string{"UC", "UA"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DMs to %v, want %v", got, want)
	}
}
