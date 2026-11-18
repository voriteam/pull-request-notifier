package notifier

import (
	"sync"
	"time"
)

// deliveryCache is in memory because a database write per webhook makes Litestream's
// startup restore grow with webhook volume. A restart empties it, which only matters
// for a delivery GitHub recorded as failed although it was processed.
type deliveryCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	now  func() time.Time
	seen map[string]time.Time
}

func newDeliveryCache(ttl time.Duration) *deliveryCache {
	return &deliveryCache{ttl: ttl, now: time.Now, seen: map[string]time.Time{}}
}

// claim reports whether id is new within the TTL, and remembers it. Safe for concurrent use.
func (c *deliveryCache) claim(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, t := range c.seen {
		if now.Sub(t) >= c.ttl {
			delete(c.seen, k)
		}
	}
	if _, ok := c.seen[id]; ok {
		return false
	}
	c.seen[id] = now
	return true
}
