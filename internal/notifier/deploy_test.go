package notifier

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/voriteam/pull-request-notifier/internal/db"
	"github.com/voriteam/pull-request-notifier/internal/github"
	"github.com/voriteam/pull-request-notifier/internal/slack"
)

// fakeRepo is a linear history whose commits map by SHA to the PR that merged them.
type fakeRepo struct {
	commits []github.CommitSummary
	prs     map[string]*github.PullSummary // keyed by commit SHA
}

type fakeGitHub struct {
	repos map[string]*fakeRepo
}

func (f *fakeGitHub) index(repo, sha string) int {
	for i, c := range f.repos[repo].commits {
		if c.SHA == sha {
			return i
		}
	}
	return -1
}

func (f *fakeGitHub) CompareCommits(_ context.Context, repo, base, head string) (*github.Comparison, error) {
	b, h := f.index(repo, base), f.index(repo, head)
	switch {
	case b < 0 || h < 0:
		return &github.Comparison{Status: "diverged"}, nil
	case h == b:
		return &github.Comparison{Status: "identical"}, nil
	case h < b:
		return &github.Comparison{Status: "behind"}, nil
	}
	return &github.Comparison{Status: "ahead", Commits: f.repos[repo].commits[b+1 : h+1]}, nil
}

func (f *fakeGitHub) GetPull(_ context.Context, repo string, number int) (*github.PullSummary, error) {
	for _, pr := range f.repos[repo].prs {
		if pr != nil && pr.Number == number {
			return pr, nil
		}
	}
	return nil, fmt.Errorf("no PR %d", number)
}

func (f *fakeGitHub) ListPullsForCommit(_ context.Context, repo, sha string) ([]github.PullSummary, error) {
	if pr := f.repos[repo].prs[sha]; pr != nil {
		return []github.PullSummary{*pr}, nil
	}
	return nil, nil
}

type sentDM struct {
	user string
	text string
}

type fakeDMs struct{ sent []sentDM }

func (f *fakeDMs) PostDM(_ context.Context, user string, blocks []slack.Block, _ string) (string, error) {
	var first struct{ Text struct{ Text string } }
	b, _ := json.Marshal(blocks[0])
	_ = json.Unmarshal(b, &first)
	f.sent = append(f.sent, sentDM{user, first.Text.Text})
	return "ts", nil
}

// newRepo builds commits c0..cN, where commit cN squash-merges PR #N by the Nth
// author, or is a direct push when that author is empty.
func newRepo(authors ...string) *fakeRepo {
	r := &fakeRepo{prs: map[string]*github.PullSummary{}}
	r.commits = append(r.commits, github.CommitSummary{SHA: "c0", Message: "initial"})
	for i, author := range authors {
		n := i + 1
		sha := fmt.Sprintf("c%d", n)
		msg := "direct push"
		if author != "" {
			msg = fmt.Sprintf("Change %d (#%d)\n\nbody", n, n)
			r.prs[sha] = &github.PullSummary{
				Number: n, Title: fmt.Sprintf("Change %d", n), URL: fmt.Sprintf("https://gh/pr/%d", n),
				Author: author, Branch: fmt.Sprintf("%s/vor-%d-change", author, n), Merged: true,
			}
		}
		r.commits = append(r.commits, github.CommitSummary{SHA: sha, Message: msg})
	}
	return r
}

func newTestAnnouncer(t *testing.T, repos map[string]*fakeRepo) (*DeployAnnouncer, *db.Store, *fakeDMs) {
	t.Helper()
	store, err := db.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for _, u := range []struct {
		login, slack string
		optIn        bool
	}{{"alice", "UA", true}, {"bob", "UB", false}, {"carol", "UC", true}} {
		if err := store.UpsertUserMapping(u.login, u.slack, "", "", nil); err != nil {
			t.Fatal(err)
		}
		if err := store.SetNotifyDeploys(u.slack, u.optIn); err != nil {
			t.Fatal(err)
		}
	}
	dms := &fakeDMs{}
	return NewDeployAnnouncer([]string{"production"}, store, &fakeGitHub{repos: repos}, dms), store, dms
}

func users(sent []sentDM) []string {
	var out []string
	for _, s := range sent {
		out = append(out, s.user)
	}
	return out
}

const backend = "voriteam/backend"

func TestAnnounce_FirstRunRecordsOnly(t *testing.T) {
	a, store, dms := newTestAnnouncer(t, map[string]*fakeRepo{backend: newRepo("alice", "carol")})
	a.Announce(context.Background(), backend, "production", "c2")

	if len(dms.sent) != 0 {
		t.Errorf("sent %v on first run", dms.sent)
	}
	if sha, _ := store.GetDeployAnnouncement(backend, "production"); sha != "c2" {
		t.Errorf("recorded %q, want c2", sha)
	}
}

func TestAnnounce_RangeNotifiesOptedInAuthorsOnce(t *testing.T) {
	// c1 alice, c2 bob (opted out), c3 dave (unlinked), c4 direct push, c5 carol, c6 alice.
	repo := newRepo("alice", "bob", "dave", "", "carol", "alice")
	repo.commits[5].Message = "Merge pull request from carol/branch" // unsquashed: found via commit lookup
	a, store, dms := newTestAnnouncer(t, map[string]*fakeRepo{backend: repo})
	ctx := context.Background()

	a.Announce(ctx, backend, "production", "c0")
	a.Announce(ctx, backend, "production", "c6")

	if got, want := users(dms.sent), []string{"UA", "UC", "UA"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DMs to %v, want %v", got, want)
	}
	if want := "🚀 <https://gh/pr/1|backend#1 Change 1> is live in production"; dms.sent[0].text != want {
		t.Errorf("DM text %q, want %q", dms.sent[0].text, want)
	}
	if sha, _ := store.GetDeployAnnouncement(backend, "production"); sha != "c6" {
		t.Errorf("recorded %q, want c6", sha)
	}

	// A rerun of the same deploy, and an older deploy arriving late, change nothing.
	a.Announce(ctx, backend, "production", "c6")
	a.Announce(ctx, backend, "production", "c3")
	if len(dms.sent) != 3 {
		t.Errorf("re-sent: %v", users(dms.sent))
	}
	if sha, _ := store.GetDeployAnnouncement(backend, "production"); sha != "c6" {
		t.Errorf("recorded %q after stale event, want c6", sha)
	}
}

func TestAnnounce_RerunAfterLostRecordDoesNotDoubleSend(t *testing.T) {
	a, store, dms := newTestAnnouncer(t, map[string]*fakeRepo{backend: newRepo("alice")})
	ctx := context.Background()
	a.Announce(ctx, backend, "production", "c0")
	a.Announce(ctx, backend, "production", "c1")

	// Simulate a crash after the DM but before the deploy was recorded.
	if err := store.SetDeployAnnouncement(backend, "production", "c0"); err != nil {
		t.Fatal(err)
	}
	a.Announce(ctx, backend, "production", "c1")
	if len(dms.sent) != 1 {
		t.Errorf("sent %d DMs, want 1", len(dms.sent))
	}
}

func TestAnnounce_DivergedRecordsWithoutNotifying(t *testing.T) {
	a, store, dms := newTestAnnouncer(t, map[string]*fakeRepo{backend: newRepo("alice")})
	ctx := context.Background()
	a.Announce(ctx, backend, "production", "c0")
	a.Announce(ctx, backend, "production", "force-pushed")

	if len(dms.sent) != 0 {
		t.Errorf("sent %v", dms.sent)
	}
	if sha, _ := store.GetDeployAnnouncement(backend, "production"); sha != "force-pushed" {
		t.Errorf("recorded %q", sha)
	}
}

func TestAnnounce_SeparateRepos(t *testing.T) {
	a, _, dms := newTestAnnouncer(t, map[string]*fakeRepo{
		backend:             newRepo("alice"),
		"voriteam/composer": newRepo("carol"),
	})
	ctx := context.Background()
	for _, repo := range []string{backend, "voriteam/composer"} {
		a.Announce(ctx, repo, "production", "c0")
	}
	a.Announce(ctx, backend, "production", "c1")
	a.Announce(ctx, "voriteam/composer", "production", "c1")

	if got, want := users(dms.sent), []string{"UA", "UC"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DMs to %v, want %v", got, want)
	}
	if !strings.Contains(dms.sent[1].text, "|composer#1 Change 1>") {
		t.Errorf("DM does not name the repo: %q", dms.sent[1].text)
	}
}

func TestAnnounce_SamePRInTwoEnvironmentsNotifiesOncePerEnvironment(t *testing.T) {
	a, _, dms := newTestAnnouncer(t, map[string]*fakeRepo{backend: newRepo("alice")})
	ctx := context.Background()
	for _, env := range []string{"staging", "production"} {
		a.Announce(ctx, backend, env, "c0")
	}
	a.Announce(ctx, backend, "staging", "c1")
	a.Announce(ctx, backend, "staging", "c1")
	a.Announce(ctx, backend, "production", "c1")

	if len(dms.sent) != 2 {
		t.Fatalf("sent %v, want one DM per environment", users(dms.sent))
	}
	for i, env := range []string{"staging", "production"} {
		if !strings.HasSuffix(dms.sent[i].text, "is live in "+env) {
			t.Errorf("DM %d = %q, want it to name %s", i, dms.sent[i].text, env)
		}
	}
}

func TestHandleDeploymentStatus(t *testing.T) {
	fixture, err := os.ReadFile("testdata/deployment_status_success.json")
	if err != nil {
		t.Fatal(err)
	}
	event := func(edit func(map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(fixture, &m); err != nil {
			t.Fatal(err)
		}
		edit(m)
		b, _ := json.Marshal(m)
		return b
	}
	deployment := func(m map[string]any) map[string]any { return m["deployment"].(map[string]any) }
	ctx := context.Background()

	a, store, dms := newTestAnnouncer(t, map[string]*fakeRepo{backend: newRepo("alice", "carol")})
	a.Announce(ctx, backend, "production", "c0")

	a.HandleDeploymentStatus(ctx, event(func(m map[string]any) {
		m["deployment_status"].(map[string]any)["state"] = "in_progress"
	}))
	a.HandleDeploymentStatus(ctx, event(func(m map[string]any) { deployment(m)["environment"] = "staging" }))
	if len(dms.sent) != 0 {
		t.Fatalf("sent %v for an ignored event", users(dms.sent))
	}

	a.HandleDeploymentStatus(ctx, fixture)
	if got, want := users(dms.sent), []string{"UA", "UC"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DMs to %v, want %v", got, want)
	}
	if sha, _ := store.GetDeployAnnouncement(backend, "production"); sha != "c2" {
		t.Errorf("recorded %q, want c2", sha)
	}
}

func TestNewDeployAnnouncer_OffWithoutEnvironments(t *testing.T) {
	if a := NewDeployAnnouncer(nil, nil, nil, nil); a != nil {
		t.Error("want nil announcer when no environments are configured")
	}
}

func TestLinearKeys(t *testing.T) {
	cases := []struct {
		branch, title string
		want          []string
	}{
		{"clintonb/vor-123-fix-thing", "Fixed the thing", []string{"VOR-123"}},
		{"vor-123", "VOR-123: Fixed the thing", []string{"VOR-123"}},
		{"clintonb/deploy-notifications", "Tell authors about deploys (ENG-9, VOR-10)", []string{"ENG-9", "VOR-10"}},
		{"main", "Bump node-18 image", nil},
	}
	for _, tc := range cases {
		if got := linearKeys(tc.branch, tc.title); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("linearKeys(%q, %q) = %v, want %v", tc.branch, tc.title, got, tc.want)
		}
	}
}
