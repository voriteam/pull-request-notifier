// Package rotation posts a weekly "who's on duty" announcement to a Slack
// channel, cycling through a fixed list of members.
package rotation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// postWindowMinutes is how long after the top of the post hour the scheduler
// will still fire. Combined with the DB dedupe this tolerates ticker drift and
// brief downtime without double-posting.
const postWindowMinutes = 5

// Member is one person in the rotation.
type Member struct {
	SlackUserID string
	Name        string
}

// ParseMembers parses a comma-separated list of "SLACK_USER_ID:Name" entries
// (e.g. "U123:Jada,U456:Kayla"). Order is the rotation order.
func ParseMembers(s string) ([]Member, error) {
	var out []Member
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, name, ok := strings.Cut(part, ":")
		id, name = strings.TrimSpace(id), strings.TrimSpace(name)
		if !ok || id == "" || name == "" {
			return nil, fmt.Errorf("invalid rotation member %q (want SLACK_USER_ID:Name)", part)
		}
		out = append(out, Member{SlackUserID: id, Name: name})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no rotation members")
	}
	return out, nil
}

// Config describes a single weekly rotation.
type Config struct {
	ChannelID string
	Members   []Member
	// StartDate is the date (YYYY-MM-DD) of a week in which Members[0] is the owner.
	StartDate string
	Weekday   time.Weekday
	Hour      int
	Location  *time.Location
	// Message is the text shown before the owner's @-mention.
	Message string
}

type rotationStore interface {
	AlreadyPostedRotation(channelID, localDate string) (bool, error)
	MarkRotationPosted(channelID, localDate string) error
}

type channelPoster interface {
	PostChannelMessage(ctx context.Context, channelID, text string) (string, error)
}

// Scheduler posts the rotation announcement once a week.
type Scheduler struct {
	cfg   Config
	start time.Time // StartDate at midnight UTC, for whole-day arithmetic
	store rotationStore
	slack channelPoster

	now      func() time.Time
	interval time.Duration
}

// New constructs a Scheduler. It returns an error if the config is invalid.
func New(cfg Config, store rotationStore, sl channelPoster) (*Scheduler, error) {
	if cfg.ChannelID == "" {
		return nil, fmt.Errorf("rotation channel ID is required")
	}
	if len(cfg.Members) == 0 {
		return nil, fmt.Errorf("rotation needs at least one member")
	}
	if cfg.Hour < 0 || cfg.Hour > 23 {
		return nil, fmt.Errorf("rotation hour %d out of range", cfg.Hour)
	}
	start, err := time.Parse("2006-01-02", cfg.StartDate)
	if err != nil {
		return nil, fmt.Errorf("invalid rotation start date %q: %w", cfg.StartDate, err)
	}
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	return &Scheduler{
		cfg:      cfg,
		start:    start,
		store:    store,
		slack:    sl,
		now:      time.Now,
		interval: time.Minute,
	}, nil
}

// Start runs the scheduler until ctx is cancelled. Intended to be run in a goroutine.
func (s *Scheduler) Start(ctx context.Context) {
	slog.Info("rotation scheduler started", "channel", s.cfg.ChannelID, "weekday", s.cfg.Weekday.String(), "hour", s.cfg.Hour, "tz", s.cfg.Location.String(), "members", len(s.cfg.Members))
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
	local := now.In(s.cfg.Location)
	if local.Weekday() != s.cfg.Weekday || local.Hour() != s.cfg.Hour || local.Minute() >= postWindowMinutes {
		return
	}

	date := local.Format("2006-01-02")
	already, err := s.store.AlreadyPostedRotation(s.cfg.ChannelID, date)
	if err != nil {
		slog.Error("rotation: dedupe check", "channel", s.cfg.ChannelID, "err", err)
		return
	}
	if already {
		return
	}

	owner := s.OwnerFor(local)
	if _, err := s.slack.PostChannelMessage(ctx, s.cfg.ChannelID, s.MessageFor(owner)); err != nil {
		// Don't mark posted — retry within the window.
		slog.Error("rotation: post message", "channel", s.cfg.ChannelID, "err", err)
		return
	}
	slog.Info("rotation: posted", "channel", s.cfg.ChannelID, "owner", owner.SlackUserID)

	if err := s.store.MarkRotationPosted(s.cfg.ChannelID, date); err != nil {
		slog.Error("rotation: mark posted", "channel", s.cfg.ChannelID, "err", err)
	}
}

// OwnerFor returns the member on duty for the week containing t (in the
// rotation's timezone). Weeks are counted from StartDate.
func (s *Scheduler) OwnerFor(t time.Time) Member {
	local := t.In(s.cfg.Location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	days := int(day.Sub(s.start).Hours() / 24)
	weeks := days / 7
	if days < 0 && days%7 != 0 {
		weeks-- // floor division for dates before StartDate
	}
	n := len(s.cfg.Members)
	return s.cfg.Members[((weeks%n)+n)%n]
}

// MessageFor renders the announcement, e.g.:
//
//	:iphone: _This week's owner:_ <@U123>
//
//	Rotation: Jada → Kayla → Taylor → (repeat)
func (s *Scheduler) MessageFor(owner Member) string {
	names := make([]string, len(s.cfg.Members))
	for i, m := range s.cfg.Members {
		names[i] = m.Name
	}
	return fmt.Sprintf("%s <@%s>\n\nRotation: %s → (repeat)", s.cfg.Message, owner.SlackUserID, strings.Join(names, " → "))
}
