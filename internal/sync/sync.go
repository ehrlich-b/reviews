package sync

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/ehrlich-b/reviews/internal/db"
	"github.com/ehrlich-b/reviews/internal/github"
	"github.com/ehrlich-b/reviews/internal/jira"
)

var ticketRe = regexp.MustCompile(`[A-Z]+-\d+`)

type Syncer struct {
	gh    *github.Client
	store *db.Store
	jira  *jira.Client
}

func New(gh *github.Client, store *db.Store) *Syncer {
	return &Syncer{gh: gh, store: store}
}

func (s *Syncer) SetJiraClient(c *jira.Client) {
	s.jira = c
}

type Summary struct {
	Repos       int
	Total       int
	NeedsReview int
	AuthorCourt int
	Approved    int
	Skipped     int
}

func (s *Syncer) Run(verbose bool, orgs []string) (*Summary, error) {
	viewer, err := s.gh.ViewerLogin()
	if err != nil {
		return nil, fmt.Errorf("get viewer: %w", err)
	}
	log.Printf("syncing as %s", viewer)

	repos, err := s.gh.DiscoverRepos(orgs)
	if err != nil {
		return nil, fmt.Errorf("discover repos: %w", err)
	}

	// Prune repos no longer in discovery (e.g. switched org, repo deleted)
	prev, err := s.store.ListSyncedRepos()
	if err != nil {
		return nil, fmt.Errorf("list synced repos: %w", err)
	}
	discovered := map[string]bool{}
	for _, r := range repos {
		discovered[r] = true
	}
	for _, r := range prev {
		if !discovered[r] {
			s.store.PrunePRs(r, nil)
			s.store.DeleteSyncState(r)
		}
	}

	if len(repos) == 0 {
		log.Printf("no repos to sync")
		return &Summary{}, nil
	}
	log.Printf("syncing %d repos", len(repos))

	allPRs, err := s.gh.FetchRepoPRs(repos)
	if err != nil {
		return nil, fmt.Errorf("fetch PRs: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	sum := &Summary{Repos: len(repos)}

	for repo, prs := range allPRs {
		var openNumbers []int
		for _, pr := range prs {
			openNumbers = append(openNumbers, pr.Number)
			row := classify(pr, "", repo, now)
			if err := s.store.UpsertPR(row); err != nil {
				return nil, fmt.Errorf("upsert %s#%d: %w", repo, pr.Number, err)
			}
			// Track everyone who's authored or engaged on a PR we've seen
			if row.Author != "" {
				s.store.AddKnownAuthor(row.Author)
			}
			if row.EngagedUsers != nil {
				var engaged []string
				if err := json.Unmarshal([]byte(*row.EngagedUsers), &engaged); err == nil {
					for _, u := range engaged {
						s.store.AddKnownAuthor(u)
					}
				}
			}
			sum.Total++
			switch row.TriageBucket {
			case "needs_review":
				sum.NeedsReview++
			case "author_court":
				sum.AuthorCourt++
			case "approved":
				sum.Approved++
			case "skipped":
				sum.Skipped++
			}
		}
		if err := s.store.PrunePRs(repo, openNumbers); err != nil {
			return nil, fmt.Errorf("prune %s: %w", repo, err)
		}
		if err := s.store.SetSyncState(repo, now); err != nil {
			return nil, fmt.Errorf("sync state %s: %w", repo, err)
		}

		if verbose {
			log.Printf("  %s: %d open PRs", repo, len(prs))
		}
	}

	// Clean up repos that were requested but not returned (deleted/inaccessible)
	for _, r := range repos {
		if _, ok := allPRs[r]; !ok {
			s.store.PrunePRs(r, nil)
			s.store.DeleteSyncState(r)
		}
	}

	// Jira enrichment (best-effort)
	if s.jira != nil {
		s.syncJira(now)
	}

	return sum, nil
}

func (s *Syncer) syncJira(syncedAt string) {
	prs, err := s.store.ListPRs()
	if err != nil {
		log.Printf("jira sync: list PRs: %v", err)
		return
	}
	keySet := map[string]bool{}
	for _, pr := range prs {
		if pr.TicketKey != nil {
			keySet[*pr.TicketKey] = true
		}
	}
	if len(keySet) == 0 {
		return
	}
	var keys []string
	for k := range keySet {
		keys = append(keys, k)
	}
	issues, err := s.jira.FetchIssues(keys)
	if err != nil {
		log.Printf("jira sync: fetch: %v", err)
		return
	}
	for _, issue := range issues {
		var epicKey, epicSummary *string
		if issue.EpicKey != "" {
			epicKey = &issue.EpicKey
			epicSummary = &issue.EpicSummary
		}
		if err := s.store.UpsertJiraIssue(issue.Key, issue.Summary, issue.Status, epicKey, epicSummary, syncedAt); err != nil {
			log.Printf("jira sync: upsert %s: %v", issue.Key, err)
		}
	}
	log.Printf("jira sync: updated %d issues", len(issues))
}

func classify(pr github.PullRequestNode, viewer, repo, syncedAt string) *db.PullRequest {
	author := ""
	var authorAvatar *string
	if pr.Author != nil {
		author = pr.Author.Login
		if pr.Author.AvatarUrl != "" {
			authorAvatar = &pr.Author.AvatarUrl
		}
	}

	row := &db.PullRequest{
		Repo:         repo,
		Number:       pr.Number,
		Title:        pr.Title,
		Author:       author,
		AuthorAvatar: authorAvatar,
		URL:          pr.URL,
		Draft:        pr.IsDraft,
		CommentCount: len(pr.Comments.Nodes),
		Additions:    pr.Additions,
		Deletions:    pr.Deletions,
		CreatedAt:    pr.CreatedAt,
		UpdatedAt:    pr.UpdatedAt,
		SyncedAt:     syncedAt,
	}

	// Ticket key
	if m := ticketRe.FindString(pr.Title); m != "" {
		row.TicketKey = &m
	}

	// Last commit
	if len(pr.Commits.Nodes) > 0 {
		c := pr.Commits.Nodes[0]
		row.LastCommitAt = &c.Commit.CommittedDate
		if c.Commit.StatusCheckRollup != nil {
			st := strings.ToLower(c.Commit.StatusCheckRollup.State)
			row.CIStatus = &st
		}
	}

	// Review decision
	if pr.ReviewDecision != nil {
		rd := strings.ToLower(*pr.ReviewDecision)
		row.ReviewStatus = &rd
	}

	// Approvers
	var approvers []string
	for _, r := range pr.LatestReviews.Nodes {
		if r.Author != nil && r.State == "APPROVED" && r.Author.Login != author {
			approvers = append(approvers, r.Author.Login)
		}
	}
	if len(approvers) > 0 {
		b, _ := json.Marshal(approvers)
		s := string(b)
		row.Approvers = &s
	}

	// Engaged users (reviewed or commented, excluding author and bots)
	engagedSeen := map[string]bool{}
	var engaged []string
	for _, r := range pr.Reviews.Nodes {
		if r.Author != nil && r.Author.Login != author && !isBot(r.Author.Login) && !engagedSeen[r.Author.Login] {
			engagedSeen[r.Author.Login] = true
			engaged = append(engaged, r.Author.Login)
		}
	}
	for _, c := range pr.Comments.Nodes {
		if c.Author != nil && c.Author.Login != author && !isBot(c.Author.Login) && !engagedSeen[c.Author.Login] {
			engagedSeen[c.Author.Login] = true
			engaged = append(engaged, c.Author.Login)
		}
	}
	if len(engaged) > 0 {
		b, _ := json.Marshal(engaged)
		s := string(b)
		row.EngagedUsers = &s
	}

	// Last reviewer activity
	lastActivity := lastReviewerActivity(pr, author)
	if lastActivity != "" {
		row.LastReviewActivityAt = &lastActivity
	}

	// Triage
	bucket, reason := triage(pr, author, viewer)
	row.TriageBucket = bucket
	row.TriageReason = &reason

	return row
}

func triage(pr github.PullRequestNode, author, viewer string) (bucket, reason string) {
	// 1. Skip drafts and WIP
	if pr.IsDraft {
		return "skipped", "draft"
	}
	if matched, _ := regexp.MatchString(`(?i)^WIP\b`, pr.Title); matched {
		return "skipped", "WIP"
	}

	// 2. Own PRs
	if author == viewer {
		if pr.ReviewDecision != nil && *pr.ReviewDecision == "APPROVED" {
			return "approved", "your PR — approved, merge it"
		}
		return "skipped", "your PR"
	}

	// 3. Approved
	if pr.ReviewDecision != nil && *pr.ReviewDecision == "APPROVED" {
		return "approved", "approved"
	}

	// 4. Timestamps
	lastCommit := ""
	if len(pr.Commits.Nodes) > 0 {
		lastCommit = pr.Commits.Nodes[0].Commit.CommittedDate
	}
	lastActivity := lastReviewerActivity(pr, author)

	// 5. Review state
	hasReviews := false
	changesRequested := false
	for _, r := range pr.Reviews.Nodes {
		if r.Author != nil && r.Author.Login != author && !isBot(r.Author.Login) {
			hasReviews = true
		}
	}
	for _, r := range pr.LatestReviews.Nodes {
		if r.Author != nil && r.Author.Login != author && r.State == "CHANGES_REQUESTED" {
			changesRequested = true
		}
	}

	// 6. Classify
	newCommits := lastCommit != "" && lastActivity != "" && lastCommit > lastActivity

	if !hasReviews {
		return "needs_review", "needs first review"
	}
	if changesRequested && newCommits {
		return "needs_review", "author pushed fixes — re-review"
	}
	if changesRequested {
		return "author_court", "changes requested, no new commits"
	}
	if newCommits {
		return "needs_review", "new commits since review"
	}
	return "author_court", "reviewed, no new commits"
}

func lastReviewerActivity(pr github.PullRequestNode, author string) string {
	var latest string
	for _, r := range pr.Reviews.Nodes {
		if r.Author != nil && r.Author.Login != author && !isBot(r.Author.Login) && r.SubmittedAt > latest {
			latest = r.SubmittedAt
		}
	}
	for _, c := range pr.Comments.Nodes {
		if c.Author != nil && c.Author.Login != author && !isBot(c.Author.Login) && c.CreatedAt > latest {
			latest = c.CreatedAt
		}
	}
	return latest
}

func isBot(login string) bool {
	return strings.HasSuffix(login, "[bot]")
}
