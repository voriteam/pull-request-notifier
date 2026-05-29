// Package reminder sends linked users a periodic digest of pull requests
// awaiting their review, at configured local-time hours.
package reminder

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/voriteam/pull-request-notifier/internal/db"
	"github.com/voriteam/pull-request-notifier/internal/github"
	"github.com/voriteam/pull-request-notifier/internal/slack"
)

// reminderWindowMinutes is how long after the top of a reminder hour the
// scheduler will still fire. Combined with the DB dedupe this tolerates ticker
// drift and brief downtime without double-sending.
const reminderWindowMinutes = 5

// reminderStore is the subset of *db.Store the scheduler needs.
type reminderStore interface {
	ListAllMappings() ([]db.UserMappingSummary, error)
	AlreadyReminded(slackUserID, localDate string, hour int) (bool, error)
	MarkReminderSent(slackUserID, localDate string, hour int) error
}

// prLister is the subset of *github.Client the scheduler needs.
type prLister interface {
	ListReviewRequestedPRs(ctx context.Context, username string) ([]github.AssignedPR, error)
}

// dmSender is the subset of *slack.Client the scheduler needs.
type dmSender interface {
	GetUserTimezone(ctx context.Context, userID string) *time.Location
	PostDM(ctx context.Context, userID string, blocks []slack.Block, fallbackText string) (string, error)
}

// Scheduler periodically reminds users about PRs awaiting their review.
type Scheduler struct {
	store        reminderStore
	gh           prLister
	slack        dmSender
	hours        map[int]bool
	weekdaysOnly bool
	defaultTZ    *time.Location

	now      func() time.Time
	interval time.Duration
}

// New constructs a Scheduler. defaultTZ is used when a user's timezone cannot be
// determined; a nil defaultTZ falls back to UTC.
func New(store reminderStore, gh prLister, sl dmSender, hours []int, weekdaysOnly bool, defaultTZ *time.Location) *Scheduler {
	hourSet := make(map[int]bool, len(hours))
	for _, h := range hours {
		hourSet[h] = true
	}
	if defaultTZ == nil {
		defaultTZ = time.UTC
	}
	return &Scheduler{
		store:        store,
		gh:           gh,
		slack:        sl,
		hours:        hourSet,
		weekdaysOnly: weekdaysOnly,
		defaultTZ:    defaultTZ,
		now:          time.Now,
		interval:     time.Minute,
	}
}

// Start runs the scheduler until ctx is cancelled. Intended to be run in a goroutine.
func (s *Scheduler) Start(ctx context.Context) {
	slog.Info("reminder scheduler started", "hours", keys(s.hours), "weekdays_only", s.weekdaysOnly, "default_tz", s.defaultTZ.String())
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runOnce(ctx, s.now())
		}
	}
}

func (s *Scheduler) runOnce(ctx context.Context, now time.Time) {
	mappings, err := s.store.ListAllMappings()
	if err != nil {
		slog.Error("reminder: list mappings", "err", err)
		return
	}
	for _, m := range mappings {
		s.remindUser(ctx, now, m)
	}
}

func (s *Scheduler) remindUser(ctx context.Context, now time.Time, m db.UserMappingSummary) {
	loc := s.slack.GetUserTimezone(ctx, m.SlackUserID)
	if loc == nil {
		loc = s.defaultTZ
	}
	local := now.In(loc)

	if s.weekdaysOnly {
		if wd := local.Weekday(); wd == time.Saturday || wd == time.Sunday {
			return
		}
	}

	hour := local.Hour()
	if !s.hours[hour] || local.Minute() >= reminderWindowMinutes {
		return
	}

	date := local.Format("2006-01-02")
	already, err := s.store.AlreadyReminded(m.SlackUserID, date, hour)
	if err != nil {
		slog.Error("reminder: dedupe check", "slack_user_id", m.SlackUserID, "err", err)
		return
	}
	if already {
		return
	}

	prs, err := s.gh.ListReviewRequestedPRs(ctx, m.GitHubUsername)
	if err != nil {
		// Don't mark sent — let the next tick within the window retry.
		slog.Error("reminder: list review-requested prs", "github", m.GitHubUsername, "err", err)
		return
	}

	if len(prs) > 0 {
		blocks := slack.ReminderBlocks(prs)
		fallback := fmt.Sprintf("You have %d PR(s) awaiting your review", len(prs))
		if _, err := s.slack.PostDM(ctx, m.SlackUserID, blocks, fallback); err != nil {
			// Don't mark sent — retry within the window.
			slog.Error("reminder: send dm", "slack_user_id", m.SlackUserID, "err", err)
			return
		}
		slog.Info("reminder: sent", "slack_user_id", m.SlackUserID, "github", m.GitHubUsername, "pr_count", len(prs), "hour", hour)
	}

	if err := s.store.MarkReminderSent(m.SlackUserID, date, hour); err != nil {
		slog.Error("reminder: mark sent", "slack_user_id", m.SlackUserID, "err", err)
	}
}

func keys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
