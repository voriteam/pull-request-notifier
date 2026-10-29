package notifier

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/voriteam/pull-request-notifier/internal/db"
	"github.com/voriteam/pull-request-notifier/internal/github"
	"github.com/voriteam/pull-request-notifier/internal/slack"
)

type deployGitHub interface {
	CompareCommits(ctx context.Context, repo, base, head string) (*github.Comparison, error)
	GetPull(ctx context.Context, repo string, number int) (*github.PullSummary, error)
	ListPullsForCommit(ctx context.Context, repo, sha string) ([]github.PullSummary, error)
}

type dmPoster interface {
	PostDM(ctx context.Context, userID string, blocks []slack.Block, fallbackText string) (string, error)
}

// DeployAnnouncer DMs opted-in authors when their merged PRs are deployed.
type DeployAnnouncer struct {
	environments map[string]bool
	store        *db.Store
	gh           deployGitHub
	slack        dmPoster

	// Serializes announcements so overlapping deploys can neither double-send
	// nor move a recorded SHA backward.
	mu sync.Mutex
}

// NewDeployAnnouncer returns a DeployAnnouncer for successful deployments to
// the given environments, or nil when there are none.
func NewDeployAnnouncer(environments []string, store *db.Store, gh deployGitHub, sl dmPoster) *DeployAnnouncer {
	if len(environments) == 0 {
		return nil
	}
	envs := make(map[string]bool, len(environments))
	for _, e := range environments {
		envs[e] = true
	}
	return &DeployAnnouncer{environments: envs, store: store, gh: gh, slack: sl}
}

type deploymentStatusEvent struct {
	DeploymentStatus struct {
		State string `json:"state"`
	} `json:"deployment_status"`
	Deployment struct {
		SHA         string `json:"sha"`
		Environment string `json:"environment"`
	} `json:"deployment"`
	Repository struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
}

// HandleDeploymentStatus announces a successful deployment to a configured environment.
func (a *DeployAnnouncer) HandleDeploymentStatus(ctx context.Context, body []byte) {
	var evt deploymentStatusEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		slog.Error("deployment_status.parse_failed", "err", err)
		return
	}
	if evt.DeploymentStatus.State != "success" || !a.environments[evt.Deployment.Environment] {
		return
	}
	a.Announce(ctx, evt.Repository.FullName, evt.Repository.DefaultBranch, evt.Deployment.Environment, evt.Deployment.SHA)
}

// Announce handles sha being deployed to the repo's environment. It announces
// the PRs merged into branch since the environment's previously announced commit. The
// first call for an environment only records sha. A sha that is not ahead of
// the previous one is ignored, so out-of-order and repeated calls are safe.
func (a *DeployAnnouncer) Announce(ctx context.Context, repo, branch, environment, sha string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	ctx, span := tracer.Start(ctx, "announceDeploy", trace.WithAttributes(
		attribute.String("github.repository", repo),
		attribute.String("github.environment", environment),
		attribute.String("github.head_sha", sha),
	))
	defer span.End()
	log := slog.With("github.repository", repo, "github.environment", environment, "github.head_sha", sha)

	prev, err := a.store.GetDeployAnnouncement(repo, environment)
	if err != nil {
		log.Error("deploy.get_announcement_failed", "err", err)
		return
	}
	if prev == "" {
		a.record(log, repo, environment, sha)
		log.Info("deploy.first_run")
		return
	}

	cmp, err := a.gh.CompareCommits(ctx, repo, prev, sha)
	if err != nil {
		log.Error("deploy.compare_failed", "github.base_sha", prev, "err", err)
		return
	}
	span.SetAttributes(
		attribute.String("deploy.compare_status", cmp.Status),
		attribute.Int("deploy.commits", len(cmp.Commits)),
	)
	switch cmp.Status {
	case "ahead":
	case "diverged":
		a.record(log, repo, environment, sha)
		log.Warn("deploy.diverged", "github.base_sha", prev)
		return
	default:
		log.Info("deploy.skipped", "reason", "not ahead of last announcement", "deploy.compare_status", cmp.Status, "github.base_sha", prev)
		return
	}

	pending := a.pendingByUser(ctx, log, repo, branch, environment, cmp.Commits)
	notified, dms := 0, 0
	for _, p := range pending {
		if a.send(ctx, log, repo, environment, p) {
			notified += len(p.prs)
			dms++
		}
	}
	span.SetAttributes(attribute.Int("deploy.notified", notified), attribute.Int("deploy.dms", dms))

	a.record(log, repo, environment, sha)
	log.Info("deploy.announced", "github.base_sha", prev, "deploy.commits", len(cmp.Commits), "deploy.notified", notified, "deploy.dms", dms)
}

func (a *DeployAnnouncer) record(log *slog.Logger, repo, environment, sha string) {
	if err := a.store.SetDeployAnnouncement(repo, environment, sha); err != nil {
		log.Error("deploy.record_failed", "err", err)
	}
}

type pendingDM struct {
	slackUserID string
	prs         []*github.PullSummary
}

// pendingByUser returns the PRs in commits that each opted-in author hasn't
// been told about, grouped by Slack user in order of first merge.
func (a *DeployAnnouncer) pendingByUser(ctx context.Context, log *slog.Logger, repo, branch, environment string, commits []github.CommitSummary) []*pendingDM {
	var pending []*pendingDM
	byUser := make(map[string]*pendingDM)
	seen := make(map[int]bool)
	for _, commit := range commits {
		pr, err := a.findPull(ctx, repo, branch, commit)
		if err != nil {
			log.Error("deploy.find_pull_failed", "github.commit_sha", commit.SHA, "err", err)
			continue
		}
		if pr == nil || seen[pr.Number] {
			continue
		}
		seen[pr.Number] = true
		prLog := log.With("github.pr_number", pr.Number, "github.author", pr.Author)

		slackUserID, err := a.store.GetDeployNotifySlackID(pr.Author)
		if err != nil {
			prLog.Error("deploy.lookup_mapping_failed", "err", err)
			continue
		}
		if slackUserID == "" {
			continue
		}
		done, err := a.store.DeployNotified(repo, environment, pr.Number, slackUserID)
		if err != nil {
			prLog.Error("deploy.lookup_notified_failed", "err", err)
			continue
		}
		if done {
			continue
		}

		p := byUser[slackUserID]
		if p == nil {
			p = &pendingDM{slackUserID: slackUserID}
			byUser[slackUserID] = p
			pending = append(pending, p)
		}
		p.prs = append(p.prs, pr)
	}
	return pending
}

// send DMs one user about all their pending PRs, then marks them notified.
// A failed send marks none of them.
func (a *DeployAnnouncer) send(ctx context.Context, log *slog.Logger, repo, environment string, p *pendingDM) bool {
	_, repoName, _ := github.SplitRepo(repo)
	prs := make([]slack.DeployedPR, len(p.prs))
	numbers := make([]int, len(p.prs))
	for i, pr := range p.prs {
		prs[i] = slack.DeployedPR{
			RepoName: repoName, Number: pr.Number, Title: pr.Title, URL: pr.URL,
			LinearKeys: linearKeys(pr.Branch, pr.Title),
		}
		numbers[i] = pr.Number
	}
	log = log.With("slack.user_id", p.slackUserID, "github.pr_numbers", numbers)

	fallback := fmt.Sprintf("%s is live in %s", p.prs[0].Title, environment)
	if len(prs) > 1 {
		fallback = fmt.Sprintf("%d of your pull requests are live in %s", len(prs), environment)
	}
	if _, err := a.slack.PostDM(ctx, p.slackUserID, slack.DeployedBlocks(environment, prs), fallback); err != nil {
		trace.SpanFromContext(ctx).RecordError(err)
		log.Error("deploy.send_dm_failed", "err", err)
		return false
	}
	for _, n := range numbers {
		if err := a.store.MarkDeployNotified(repo, environment, n, p.slackUserID); err != nil {
			log.Error("deploy.mark_notified_failed", "github.pr_number", n, "err", err)
		}
	}
	log.Info("deploy.notified")
	return true
}

var squashPRNumber = regexp.MustCompile(`\(#(\d+)\)\s*$`)

// findPull returns the PR merged into branch that produced commit, or nil for
// a direct push. A commit's PR list also holds open PRs and PRs merged into
// other branches that contain it.
func (a *DeployAnnouncer) findPull(ctx context.Context, repo, branch string, commit github.CommitSummary) (*github.PullSummary, error) {
	mergedIntoBranch := func(pr *github.PullSummary) bool { return pr.Merged && pr.BaseBranch == branch }

	subject, _, _ := strings.Cut(commit.Message, "\n")
	if m := squashPRNumber.FindStringSubmatch(subject); m != nil {
		n, _ := strconv.Atoi(m[1])
		pr, err := a.gh.GetPull(ctx, repo, n)
		if err != nil {
			return nil, err
		}
		if mergedIntoBranch(pr) {
			return pr, nil
		}
	}
	prs, err := a.gh.ListPullsForCommit(ctx, repo, commit.SHA)
	if err != nil {
		return nil, err
	}
	for i := range prs {
		if mergedIntoBranch(&prs[i]) {
			return &prs[i], nil
		}
	}
	return nil, nil
}

var (
	titleLinearKey = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-\d+\b`)
	// Linear's branch names put the key at the start of a path segment, as in user/vor-123-slug.
	branchLinearKey = regexp.MustCompile(`(?i)(?:^|/)([a-z][a-z0-9]{1,9}-\d+)`)
)

// linearKeys returns the Linear issue keys in a branch name and title, uppercased and deduped.
func linearKeys(branch, title string) []string {
	var keys []string
	seen := make(map[string]bool)
	add := func(k string) {
		k = strings.ToUpper(k)
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for _, m := range branchLinearKey.FindAllStringSubmatch(branch, -1) {
		add(m[1])
	}
	for _, k := range titleLinearKey.FindAllString(title, -1) {
		add(k)
	}
	return keys
}
