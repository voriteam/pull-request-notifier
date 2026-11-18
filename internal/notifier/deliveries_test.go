package notifier

import (
	"testing"
	"time"
)

func TestDeliveryCache_ForgetsIDsAfterTTL(t *testing.T) {
	now := time.Date(2026, 11, 18, 19, 0, 0, 0, time.UTC)
	c := newDeliveryCache(time.Hour)
	c.now = func() time.Time { return now }

	if !c.claim("guid-1") {
		t.Fatal("first claim refused")
	}
	now = now.Add(59 * time.Minute)
	if c.claim("guid-1") {
		t.Error("claim within the TTL accepted")
	}
	now = now.Add(time.Minute)
	if !c.claim("guid-1") {
		t.Error("claim after the TTL refused")
	}
	if len(c.seen) != 1 {
		t.Errorf("cache holds %d IDs, want 1", len(c.seen))
	}
}
