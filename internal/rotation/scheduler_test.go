package rotation

import (
	"context"
	"os"
	"testing"
	"time"
	_ "time/tzdata" // ensure LoadLocation works without system zoneinfo

	"github.com/voriteam/pull-request-notifier/internal/db"
)

func newTestStore(t *testing.T) *db.Store {
	t.Helper()
	f, err := os.CreateTemp("", "pr-notifier-rotation-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() { os.Remove(path) })

	store, err := db.New(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

type fakePoster struct {
	posts []string
}

func (f *fakePoster) PostChannelMessage(_ context.Context, _, text string) (string, error) {
	f.posts = append(f.posts, text)
	return "ts", nil
}

func testConfig(t *testing.T) Config {
	t.Helper()
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	members, err := ParseMembers("UJ:Jada, UK:Kayla ,UT:Taylor")
	if err != nil {
		t.Fatalf("parse members: %v", err)
	}
	return Config{
		ChannelID: "C1",
		Members:   members,
		StartDate: "2026-09-21",
		Weekday:   time.Monday,
		Hour:      9,
		Location:  loc,
		Message:   ":iphone: _This week's iOS release notes & testing owner:_",
	}
}

func TestParseMembers_Invalid(t *testing.T) {
	for _, in := range []string{"", "U1", "U1:", ":Jada", "U1:Jada,bad"} {
		if _, err := ParseMembers(in); err == nil {
			t.Errorf("ParseMembers(%q) expected error", in)
		}
	}
}

func TestOwnerFor_Cycles(t *testing.T) {
	s, err := New(testConfig(t), nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	loc := s.cfg.Location
	cases := map[string]string{
		"2026-09-21": "Jada",
		"2026-09-27": "Jada", // Sunday, same week
		"2026-09-28": "Kayla",
		"2026-10-05": "Taylor",
		"2026-10-12": "Jada",
		"2026-09-14": "Taylor", // before start date
		"2026-09-20": "Taylor",
		"2026-09-07": "Kayla",
	}
	for date, want := range cases {
		d, _ := time.ParseInLocation("2006-01-02", date, loc)
		if got := s.OwnerFor(d.Add(9 * time.Hour)).Name; got != want {
			t.Errorf("OwnerFor(%s) = %s, want %s", date, got, want)
		}
	}
}

func TestMessageFor(t *testing.T) {
	s, err := New(testConfig(t), nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	got := s.MessageFor(s.cfg.Members[0])
	want := ":iphone: _This week's iOS release notes & testing owner:_ <@UJ>\n\nRotation: Jada → Kayla → Taylor → (repeat)"
	if got != want {
		t.Errorf("MessageFor =\n%q\nwant\n%q", got, want)
	}
}

func TestRunOnce_PostsMondayAt9PTOnce(t *testing.T) {
	cfg := testConfig(t)
	poster := &fakePoster{}
	s, err := New(cfg, newTestStore(t), poster)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	at := func(date string, hour, min int) time.Time {
		d, _ := time.ParseInLocation("2006-01-02", date, cfg.Location)
		return d.Add(time.Duration(hour)*time.Hour + time.Duration(min)*time.Minute)
	}
	ctx := context.Background()

	s.runOnce(ctx, at("2026-09-28", 8, 59)) // too early
	s.runOnce(ctx, at("2026-09-29", 9, 0))  // Tuesday
	s.runOnce(ctx, at("2026-09-28", 9, 5))  // past window
	if len(poster.posts) != 0 {
		t.Fatalf("expected no posts, got %d", len(poster.posts))
	}

	s.runOnce(ctx, at("2026-09-28", 9, 0))
	s.runOnce(ctx, at("2026-09-28", 9, 1)) // deduped
	if len(poster.posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(poster.posts))
	}
	if want := ":iphone: _This week's iOS release notes & testing owner:_ <@UK>\n\nRotation: Jada → Kayla → Taylor → (repeat)"; poster.posts[0] != want {
		t.Errorf("post = %q, want %q", poster.posts[0], want)
	}

	s.runOnce(ctx, at("2026-10-05", 9, 2))
	if len(poster.posts) != 2 {
		t.Fatalf("expected 2 posts, got %d", len(poster.posts))
	}
}
