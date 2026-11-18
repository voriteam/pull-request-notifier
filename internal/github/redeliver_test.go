package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"
)

type hookServer struct {
	mu       sync.Mutex
	gets     []string
	attempts []string
}

// newHookClient serves the testdata files as consecutive pages of /app/hook/deliveries.
func newHookClient(t *testing.T, pages ...string) (*Client, *hookServer) {
	t.Helper()
	hs := &hookServer{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth == "Bearer test" {
			t.Errorf("%s %s sent the installation token, want the app JWT", r.Method, r.URL)
		}
		hs.mu.Lock()
		defer hs.mu.Unlock()
		if r.Method == http.MethodPost {
			hs.attempts = append(hs.attempts, r.URL.Path)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		page := -1
		for i := range pages {
			uri := "/app/hook/deliveries?per_page=100"
			if i > 0 {
				uri += fmt.Sprintf("&cursor=p%d", i+1)
			}
			if r.URL.RequestURI() == uri {
				page = i
			}
		}
		if page < 0 {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
			return
		}
		if page+1 < len(pages) {
			w.Header().Set("Link", fmt.Sprintf(`<%s/app/hook/deliveries?per_page=100&cursor=p%d>; rel="next"`, srv.URL, page+2))
		}
		hs.gets = append(hs.gets, r.URL.RequestURI())
		b, err := os.ReadFile(filepath.Join("testdata", pages[page]))
		if err != nil {
			t.Error(err)
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := NewClient("", "", "")
	c.baseURL = srv.URL
	c.appID = "1"
	c.privateKey = key
	c.installToken = "test"
	c.installTokenExp = time.Now().Add(time.Hour)
	return c, hs
}

func (hs *hookServer) takeAttempts() []string {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	got := hs.attempts
	hs.attempts = nil
	sort.Strings(got)
	return got
}

type fakeRedeliveryStore map[string]int

func (f fakeRedeliveryStore) RedeliveryCount(guid string) (int, error) { return f[guid], nil }
func (f fakeRedeliveryStore) RecordRedelivery(guid string) error       { f[guid]++; return nil }

func newTestRedeliverer(c *Client, store fakeRedeliveryStore, now time.Time) *Redeliverer {
	r := NewRedeliverer(c, store)
	r.now = func() time.Time { return now }
	r.startDelay = 0
	return r
}

func TestSweep_RedeliversGUIDsWithNoSuccessfulAttempt(t *testing.T) {
	c, hs := newHookClient(t, "hook_deliveries_page1.json", "hook_deliveries_page2.json")
	store := fakeRedeliveryStore{}
	r := newTestRedeliverer(c, store, time.Date(2026, 11, 18, 16, 30, 0, 0, time.UTC))

	r.sweep(context.Background(), time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC))

	// guid-b is the only GUID with a 2xx attempt.
	want := []string{
		"/app/hook/deliveries/1/attempts",
		"/app/hook/deliveries/2/attempts",
		"/app/hook/deliveries/3/attempts",
		"/app/hook/deliveries/6/attempts",
	}
	if got := hs.takeAttempts(); !reflect.DeepEqual(got, want) {
		t.Errorf("attempts %v, want %v", got, want)
	}
	if want := (fakeRedeliveryStore{"guid-a": 1, "guid-c": 1, "guid-d": 1, "guid-e": 1}); !reflect.DeepEqual(store, want) {
		t.Errorf("store %v, want %v", store, want)
	}
}

func TestSweep_RetriesAFailedRedeliveryOnALaterSweep(t *testing.T) {
	c, hs := newHookClient(t, "hook_deliveries_retry.json")
	store := fakeRedeliveryStore{"guid-r": 1, "guid-max": maxRedeliveries}
	r := newTestRedeliverer(c, store, time.Date(2026, 11, 18, 16, 20, 0, 0, time.UTC))

	r.sweep(context.Background(), time.Date(2026, 11, 18, 16, 0, 0, 0, time.UTC))

	if got := hs.takeAttempts(); !slices.Contains(got, "/app/hook/deliveries/12/attempts") {
		t.Errorf("attempts %v, want the newest guid-r delivery redelivered", got)
	}
	if store["guid-r"] != 2 {
		t.Errorf("guid-r count %d, want 2", store["guid-r"])
	}
}

func TestSweep_StopsAfterMaxRedeliveries(t *testing.T) {
	c, hs := newHookClient(t, "hook_deliveries_retry.json")
	store := fakeRedeliveryStore{"guid-max": maxRedeliveries}
	r := newTestRedeliverer(c, store, time.Date(2026, 11, 18, 16, 20, 0, 0, time.UTC))

	r.sweep(context.Background(), time.Date(2026, 11, 18, 16, 0, 0, 0, time.UTC))

	if got := hs.takeAttempts(); slices.Contains(got, "/app/hook/deliveries/10/attempts") {
		t.Errorf("attempts %v, want guid-max left alone", got)
	}
	if store["guid-max"] != maxRedeliveries {
		t.Errorf("guid-max count %d", store["guid-max"])
	}
}

func TestSweep_SkipsGUIDsWhoseNewestAttemptIsRecent(t *testing.T) {
	c, hs := newHookClient(t, "hook_deliveries_retry.json")
	store := fakeRedeliveryStore{}
	r := newTestRedeliverer(c, store, time.Date(2026, 11, 18, 16, 20, 0, 0, time.UTC))

	r.sweep(context.Background(), time.Date(2026, 11, 18, 16, 0, 0, 0, time.UTC))

	if got := hs.takeAttempts(); slices.Contains(got, "/app/hook/deliveries/13/attempts") {
		t.Errorf("attempts %v, want guid-fresh (3 minutes old) skipped", got)
	}
	if _, ok := store["guid-fresh"]; ok {
		t.Error("guid-fresh recorded")
	}
}

func TestListHookDeliveries_StopsAtCutoff(t *testing.T) {
	c, hs := newHookClient(t, "hook_deliveries_page1.json", "hook_deliveries_page2.json")

	got, err := c.ListHookDeliveries(context.Background(), time.Date(2026, 11, 18, 16, 4, 30, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, d := range got {
		ids = append(ids, d.ID)
	}
	if want := []int64{6, 5, 4}; !reflect.DeepEqual(ids, want) {
		t.Errorf("ids %v, want %v", ids, want)
	}
	if want := []string{"/app/hook/deliveries?per_page=100"}; !reflect.DeepEqual(hs.gets, want) {
		t.Errorf("requests %v, want %v", hs.gets, want)
	}
}
