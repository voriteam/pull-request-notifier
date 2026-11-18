package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/voriteam/pull-request-notifier/internal/db"
	"github.com/voriteam/pull-request-notifier/internal/slack"
)

type sampleDM struct {
	blocks   []slack.Block
	fallback string
}

type sampleArgs struct {
	repo, branch, check, url, title, author string
}

var sampleDMs = map[string]func(a sampleArgs) sampleDM{
	"check-failed": func(a sampleArgs) sampleDM {
		return sampleDM{slack.CheckRunFailedBlocks(a.check, a.url, a.repo, a.branch), fmt.Sprintf("Check %s failed", a.check)}
	},
	"review-requested": func(a sampleArgs) sampleDM {
		return sampleDM{
			slack.ReviewRequestedBlocks(a.author, a.title, a.url, 3, 42, 7, "open", nil),
			fmt.Sprintf("%s requested your review on %s", a.author, a.title),
		}
	},
}

func sampleKinds() string {
	kinds := make([]string, 0, len(sampleDMs))
	for k := range sampleDMs {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return strings.Join(kinds, ", ")
}

// runSampleDM implements `sample-dm`, which posts a notification DM in the
// same format the webhook handlers use, for debugging Slack delivery.
func runSampleDM(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("sample-dm", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		fmt.Fprintf(out, "Usage: pull-request-notifier sample-dm <kind> (--slack-user ID | --github LOGIN) [flags]\nKinds: %s\n", sampleKinds())
		fs.PrintDefaults()
	}
	slackUser := fs.String("slack-user", "", "Slack user ID to DM")
	githubUser := fs.String("github", "", "GitHub login to DM, resolved through the linked accounts in DB_PATH")
	var a sampleArgs
	fs.StringVar(&a.repo, "repo", "example-repo", "repository name shown in the DM")
	fs.StringVar(&a.branch, "branch", "example-branch", "branch name shown in the DM")
	fs.StringVar(&a.check, "check", "tests", "check name shown in the DM")
	fs.StringVar(&a.url, "url", "https://github.com", "link target for the check or pull request")
	fs.StringVar(&a.title, "title", "Sample pull request", "pull request title shown in the DM")
	fs.StringVar(&a.author, "author", "Sample Author", "pull request author shown in the DM")

	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fs.Usage()
		return errors.New("missing kind")
	}
	build, ok := sampleDMs[args[0]]
	if !ok {
		fs.Usage()
		return fmt.Errorf("unknown kind %q", args[0])
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	userID := *slackUser
	if userID == "" && *githubUser != "" {
		store, err := db.New(envOr("DB_PATH", "/data/pr-notifier.db"))
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer store.Close()
		if userID, err = store.GetMappingByGitHubUsername(*githubUser); err != nil {
			return fmt.Errorf("look up %s: %w", *githubUser, err)
		}
		if userID == "" {
			return fmt.Errorf("%s has not linked a Slack account", *githubUser)
		}
	}
	if userID == "" {
		fs.Usage()
		return errors.New("one of --slack-user or --github is required")
	}

	token := os.Getenv("SLACK_BOT_TOKEN")
	if token == "" {
		return errors.New("SLACK_BOT_TOKEN is not set")
	}
	dm := build(a)
	ts, err := slack.NewClient(token).PostDM(context.Background(), userID, dm.blocks, dm.fallback)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "sent %s DM to %s (ts %s)\n", args[0], userID, ts)
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
