package github

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// HookDelivery is one attempt to deliver a webhook. Redeliveries share the original's GUID.
type HookDelivery struct {
	ID          int64     `json:"id"`
	GUID        string    `json:"guid"`
	DeliveredAt time.Time `json:"delivered_at"`
	StatusCode  int       `json:"status_code"`
	Event       string    `json:"event"`
	Redelivery  bool      `json:"redelivery"`
}

func (d HookDelivery) succeeded() bool { return d.StatusCode >= 200 && d.StatusCode < 300 }

// ListHookDeliveries returns the app's webhook deliveries at or after cutoff, newest first.
func (c *Client) ListHookDeliveries(ctx context.Context, cutoff time.Time) ([]HookDelivery, error) {
	// The /app/hook endpoints reject installation tokens.
	token, err := c.generateJWT()
	if err != nil {
		return nil, fmt.Errorf("generate jwt: %w", err)
	}
	var out []HookDelivery
	path := "/app/hook/deliveries?per_page=100"
	for path != "" {
		var page []HookDelivery
		next, err := c.getPage(ctx, token, path, &page)
		if err != nil {
			return nil, err
		}
		for _, d := range page {
			if d.DeliveredAt.Before(cutoff) {
				return out, nil
			}
			out = append(out, d)
		}
		path = next
	}
	return out, nil
}

func (c *Client) RedeliverHook(ctx context.Context, id int64) error {
	token, err := c.generateJWT()
	if err != nil {
		return fmt.Errorf("generate jwt: %w", err)
	}
	return c.post(ctx, token, fmt.Sprintf("/app/hook/deliveries/%d/attempts", id), struct{}{}, nil)
}

type redeliveryStore interface {
	RedeliveryCount(guid string) (int, error)
	RecordRedelivery(guid string) error
}

const maxRedeliveries = 3

// Redeliverer re-requests webhooks GitHub failed to deliver, such as those sent while a deploy replaced the pod.
type Redeliverer struct {
	gh    *Client
	store redeliveryStore
	now   func() time.Time
	// The load balancer keeps failing requests for about 20s after the app listens,
	// so a sweep at startup would redeliver into the same outage.
	startDelay time.Duration
	// Also the minimum age of a GUID's newest attempt, which gives a requested redelivery time to land.
	interval time.Duration
}

func NewRedeliverer(gh *Client, store redeliveryStore) *Redeliverer {
	return &Redeliverer{gh: gh, store: store, now: time.Now, startDelay: 2 * time.Minute, interval: 5 * time.Minute}
}

// Run waits startDelay, sweeps the last 2 hours, then the last 15 minutes every interval, until ctx is done.
func (r *Redeliverer) Run(ctx context.Context) {
	if r.gh.privateKey == nil {
		slog.Info("webhook redelivery disabled: no GitHub App credentials")
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(r.startDelay):
	}
	r.sweep(ctx, r.now().Add(-2*time.Hour))
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.sweep(ctx, r.now().Add(-15*time.Minute))
		}
	}
}

func (r *Redeliverer) sweep(ctx context.Context, cutoff time.Time) {
	ctx, span := tracer.Start(ctx, "webhook.redeliver")
	defer span.End()

	deliveries, err := r.gh.ListHookDeliveries(ctx, cutoff)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		slog.Error("webhook redelivery: list deliveries failed", "err", err)
		return
	}

	newest := map[string]HookDelivery{}
	succeeded := map[string]bool{}
	for _, d := range deliveries {
		if d.succeeded() {
			succeeded[d.GUID] = true
		}
		if cur, ok := newest[d.GUID]; !ok || d.DeliveredAt.After(cur.DeliveredAt) {
			newest[d.GUID] = d
		}
	}

	failed, redelivered := 0, 0
	for guid, d := range newest {
		if succeeded[guid] {
			continue
		}
		failed++
		if r.now().Sub(d.DeliveredAt) < r.interval {
			continue
		}
		count, err := r.store.RedeliveryCount(guid)
		if err != nil {
			slog.Error("webhook redelivery: lookup failed", "github.delivery_guid", guid, "err", err)
			continue
		}
		if count >= maxRedeliveries {
			continue
		}
		if err := r.gh.RedeliverHook(ctx, d.ID); err != nil {
			slog.Error("webhook redelivery failed", "github.delivery_guid", guid, "github.delivery_id", d.ID, "github.event", d.Event, "err", err)
			continue
		}
		if err := r.store.RecordRedelivery(guid); err != nil {
			slog.Error("webhook redelivery: record failed", "github.delivery_guid", guid, "err", err)
		}
		redelivered++
	}

	span.SetAttributes(
		attribute.Int("redeliver.scanned", len(deliveries)),
		attribute.Int("redeliver.failed", failed),
		attribute.Int("redeliver.redelivered", redelivered),
	)
	slog.Info("webhook redelivery sweep", "scanned", len(deliveries), "failed", failed, "redelivered", redelivered)
}
