package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// newFixtureClient serves testdata files by request path and query.
func newFixtureClient(t *testing.T, routes map[string]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file, ok := routes[r.URL.RequestURI()]
		if !ok {
			t.Errorf("unexpected request %s", r.URL.RequestURI())
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c, _ := NewClient("", "", "")
	c.baseURL = srv.URL
	c.installToken = "test"
	c.installTokenExp = time.Now().Add(time.Hour)
	return c
}

func TestCompareCommits_FollowsPagination(t *testing.T) {
	c := newFixtureClient(t, map[string]string{
		"/repos/voriteam/backend/compare/aaa111...ccc333?per_page=100&page=1": "compare_page1.json",
		"/repos/voriteam/backend/compare/aaa111...ccc333?per_page=100&page=2": "compare_page2.json",
	})
	got, err := c.CompareCommits(context.Background(), "voriteam/backend", "aaa111", "ccc333")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "ahead" {
		t.Errorf("status %q", got.Status)
	}
	var shas []string
	for _, commit := range got.Commits {
		shas = append(shas, commit.SHA)
	}
	if want := []string{"bbb222", "bbb333", "ccc333"}; !reflect.DeepEqual(shas, want) {
		t.Errorf("commits %v, want %v", shas, want)
	}
	if want := "Fixed invoice rounding (#4101)\n\nCloses VOR-881"; got.Commits[0].Message != want {
		t.Errorf("message %q", got.Commits[0].Message)
	}
}

func TestGetPull(t *testing.T) {
	c := newFixtureClient(t, map[string]string{"/repos/voriteam/backend/pulls/4101": "pull.json"})
	got, err := c.GetPull(context.Background(), "voriteam/backend", 4101)
	if err != nil {
		t.Fatal(err)
	}
	want := PullSummary{
		Number: 4101, Title: "Fixed invoice rounding", URL: "https://github.com/voriteam/backend/pull/4101",
		Author: "alice", Branch: "alice/vor-881-invoice-rounding", BaseBranch: "master", Merged: true,
	}
	if *got != want {
		t.Errorf("got %+v, want %+v", *got, want)
	}
}

func TestListPullsForCommit(t *testing.T) {
	c := newFixtureClient(t, map[string]string{"/repos/voriteam/backend/commits/ccc333/pulls": "commit_pulls.json"})
	got, err := c.ListPullsForCommit(context.Background(), "voriteam/backend", "ccc333")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Number != 4099 || !got[0].Merged || got[1].Merged || got[1].BaseBranch != "alice/vor-870-retry" {
		t.Errorf("got %+v", got)
	}
}
