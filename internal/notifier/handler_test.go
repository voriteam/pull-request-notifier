package notifier

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voriteam/pull-request-notifier/internal/db"
)

const testSecret = "s3cret"

func newTestHandler(t *testing.T, process func(ctx context.Context, event string, body []byte)) *Handler {
	t.Helper()
	store, err := db.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	h := NewHandler(testSecret, false, store, nil, nil, nil)
	h.process = process
	return h
}

func postWebhook(t *testing.T, url, deliveryID string) {
	t.Helper()
	body := []byte(`{"action":"opened"}`)
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func startServer(t *testing.T, h *Handler) (*http.Server, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(h.HandleWebhook)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return srv, "http://" + ln.Addr().String()
}

func TestHandleWebhook_ProcessesEachDeliveryOnce(t *testing.T) {
	var calls atomic.Int32
	h := newTestHandler(t, func(context.Context, string, []byte) { calls.Add(1) })
	_, url := startServer(t, h)

	postWebhook(t, url, "guid-1")
	postWebhook(t, url, "guid-1")
	postWebhook(t, url, "guid-2")
	h.Wait()

	if got := calls.Load(); got != 2 {
		t.Errorf("processed %d times, want 2", got)
	}
}

func TestShutdown_WaitsForInFlightProcessing(t *testing.T) {
	release := make(chan struct{})
	var finished atomic.Bool
	h := newTestHandler(t, func(context.Context, string, []byte) {
		<-release
		finished.Store(true)
	})
	srv, url := startServer(t, h)

	postWebhook(t, url, "guid-1")

	drained := make(chan struct{})
	go func() {
		if err := srv.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
		h.Wait()
		close(drained)
	}()

	select {
	case <-drained:
		t.Fatal("shutdown returned while processing was in flight")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown never returned")
	}
	if !finished.Load() {
		t.Error("processing did not finish before shutdown returned")
	}
}
