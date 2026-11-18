package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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

func newHookClient(t *testing.T) (*Client, *hookServer) {
	t.Helper()
	hs := &hookServer{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth == "Bearer test" {
			t.Errorf("%s %s sent the installation token, want the app JWT", r.Method, r.URL)
		}
		hs.mu.Lock()
		defer hs.mu.Unlock()
		var file string
		switch r.Method + " " + r.URL.RequestURI() {
		case "GET /app/hook/deliveries?per_page=100":
			w.Header().Set("Link", `<`+srv.URL+`/app/hook/deliveries?per_page=100&cursor=p2>; rel="next"`)
			file = "hook_deliveries_page1.json"
		case "GET /app/hook/deliveries?per_page=100&cursor=p2":
			file = "hook_deliveries_page2.json"
		default:
			if r.Method == http.MethodPost {
				hs.attempts = append(hs.attempts, r.URL.Path)
				w.WriteHeader(http.StatusAccepted)
				return
			}
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
			return
		}
		hs.gets = append(hs.gets, r.URL.RequestURI())
		b, err := os.ReadFile(filepath.Join("testdata", file))
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

type fakeRedeliveryStore map[string]bool

func (f fakeRedeliveryStore) Redelivered(guid string) (bool, error) { return f[guid], nil }
func (f fakeRedeliveryStore) MarkRedelivered(guid string) error     { f[guid] = true; return nil }

func TestSweep_RedeliversOnlyUnrecordedFailures(t *testing.T) {
	c, hs := newHookClient(t)
	store := fakeRedeliveryStore{"guid-c": true}
	r := NewRedeliverer(c, store)

	r.sweep(context.Background(), time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC))

	// guid-a, guid-d and guid-e never got a 2xx. guid-b succeeded on redelivery. guid-c is already recorded.
	want := []string{
		"/app/hook/deliveries/1/attempts",
		"/app/hook/deliveries/2/attempts",
		"/app/hook/deliveries/6/attempts",
	}
	if got := hs.takeAttempts(); !reflect.DeepEqual(got, want) {
		t.Errorf("attempts %v, want %v", got, want)
	}
	for _, guid := range []string{"guid-a", "guid-d", "guid-e"} {
		if !store[guid] {
			t.Errorf("%s not recorded", guid)
		}
	}
	if store["guid-b"] {
		t.Error("guid-b recorded although it was delivered")
	}

	r.sweep(context.Background(), time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC))
	if got := hs.takeAttempts(); len(got) != 0 {
		t.Errorf("second sweep re-posted %v", got)
	}
}

func TestListHookDeliveries_StopsAtCutoff(t *testing.T) {
	c, hs := newHookClient(t)

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
