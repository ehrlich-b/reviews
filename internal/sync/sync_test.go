package sync

import (
	"testing"

	"github.com/ehrlich-b/reviews/internal/github"
)

func ptr[T any](v T) *T { return &v }

func review(login, state, at string) github.ReviewNode {
	return github.ReviewNode{Author: &github.Actor{Login: login}, State: state, SubmittedAt: at}
}

func comment(login, at string) github.CommentNode {
	return github.CommentNode{Author: &github.Actor{Login: login}, CreatedAt: at}
}

func commitNode(at, ciState string) github.CommitNode {
	var n github.CommitNode
	n.Commit.CommittedDate = at
	if ciState != "" {
		n.Commit.StatusCheckRollup = &struct {
			State string `json:"state"`
		}{State: ciState}
	}
	return n
}

func TestTriage(t *testing.T) {
	tests := []struct {
		name   string
		pr     github.PullRequestNode
		author string
		viewer string
		bucket string
		reason string
	}{
		{
			name:   "draft is skipped",
			pr:     github.PullRequestNode{IsDraft: true, Title: "SLIDE-1: foo"},
			author: "alice",
			viewer: "bob",
			bucket: "skipped",
			reason: "draft",
		},
		{
			name:   "WIP title is skipped",
			pr:     github.PullRequestNode{Title: "WIP: something"},
			author: "alice",
			viewer: "bob",
			bucket: "skipped",
			reason: "WIP",
		},
		{
			name:   "WIP case-insensitive",
			pr:     github.PullRequestNode{Title: "wip: lowercase"},
			author: "alice",
			viewer: "bob",
			bucket: "skipped",
			reason: "WIP",
		},
		{
			name:   "WIP must be a word — 'wipe' is not WIP",
			pr:     github.PullRequestNode{Title: "wipe out the cache"},
			author: "alice",
			viewer: "bob",
			bucket: "needs_review",
			reason: "needs first review",
		},
		{
			name:   "your own PR — not approved",
			pr:     github.PullRequestNode{Title: "my work"},
			author: "bob",
			viewer: "bob",
			bucket: "skipped",
			reason: "your PR",
		},
		{
			name: "your own PR — approved is shown for merge",
			pr: github.PullRequestNode{
				Title:          "my work",
				ReviewDecision: ptr("APPROVED"),
			},
			author: "bob",
			viewer: "bob",
			bucket: "approved",
			reason: "your PR — approved, merge it",
		},
		{
			name: "approved by someone else",
			pr: github.PullRequestNode{
				Title:          "feature",
				ReviewDecision: ptr("APPROVED"),
			},
			author: "alice",
			viewer: "bob",
			bucket: "approved",
			reason: "approved",
		},
		{
			name:   "no reviews — needs first review",
			pr:     github.PullRequestNode{Title: "feature"},
			author: "alice",
			viewer: "bob",
			bucket: "needs_review",
			reason: "needs first review",
		},
		{
			name: "only bot reviews don't count",
			pr: func() github.PullRequestNode {
				p := github.PullRequestNode{Title: "feature"}
				p.Reviews.Nodes = []github.ReviewNode{review("dependabot[bot]", "COMMENTED", "2025-01-01T00:00:00Z")}
				return p
			}(),
			author: "alice",
			viewer: "bob",
			bucket: "needs_review",
			reason: "needs first review",
		},
		{
			name: "author's own review doesn't count",
			pr: func() github.PullRequestNode {
				p := github.PullRequestNode{Title: "feature"}
				p.Reviews.Nodes = []github.ReviewNode{review("alice", "COMMENTED", "2025-01-01T00:00:00Z")}
				return p
			}(),
			author: "alice",
			viewer: "bob",
			bucket: "needs_review",
			reason: "needs first review",
		},
		{
			name: "changes requested, no new commits",
			pr: func() github.PullRequestNode {
				p := github.PullRequestNode{Title: "feature"}
				p.Reviews.Nodes = []github.ReviewNode{review("carol", "CHANGES_REQUESTED", "2025-01-02T00:00:00Z")}
				p.LatestReviews.Nodes = []github.ReviewNode{review("carol", "CHANGES_REQUESTED", "2025-01-02T00:00:00Z")}
				p.Commits.Nodes = []github.CommitNode{commitNode("2025-01-01T00:00:00Z", "")}
				return p
			}(),
			author: "alice",
			viewer: "bob",
			bucket: "author_court",
			reason: "changes requested, no new commits",
		},
		{
			name: "changes requested, author pushed new commits",
			pr: func() github.PullRequestNode {
				p := github.PullRequestNode{Title: "feature"}
				p.Reviews.Nodes = []github.ReviewNode{review("carol", "CHANGES_REQUESTED", "2025-01-01T00:00:00Z")}
				p.LatestReviews.Nodes = []github.ReviewNode{review("carol", "CHANGES_REQUESTED", "2025-01-01T00:00:00Z")}
				p.Commits.Nodes = []github.CommitNode{commitNode("2025-01-02T00:00:00Z", "")}
				return p
			}(),
			author: "alice",
			viewer: "bob",
			bucket: "needs_review",
			reason: "author pushed fixes — re-review",
		},
		{
			name: "reviewed, no new commits",
			pr: func() github.PullRequestNode {
				p := github.PullRequestNode{Title: "feature"}
				p.Reviews.Nodes = []github.ReviewNode{review("carol", "COMMENTED", "2025-01-02T00:00:00Z")}
				p.Commits.Nodes = []github.CommitNode{commitNode("2025-01-01T00:00:00Z", "")}
				return p
			}(),
			author: "alice",
			viewer: "bob",
			bucket: "author_court",
			reason: "reviewed, no new commits",
		},
		{
			name: "new commits since review",
			pr: func() github.PullRequestNode {
				p := github.PullRequestNode{Title: "feature"}
				p.Reviews.Nodes = []github.ReviewNode{review("carol", "COMMENTED", "2025-01-01T00:00:00Z")}
				p.Commits.Nodes = []github.CommitNode{commitNode("2025-01-02T00:00:00Z", "")}
				return p
			}(),
			author: "alice",
			viewer: "bob",
			bucket: "needs_review",
			reason: "new commits since review",
		},
		{
			name: "comment from non-author counts as activity",
			pr: func() github.PullRequestNode {
				p := github.PullRequestNode{Title: "feature"}
				p.Reviews.Nodes = []github.ReviewNode{review("carol", "COMMENTED", "2025-01-01T00:00:00Z")}
				p.Comments.Nodes = []github.CommentNode{comment("dave", "2025-01-03T00:00:00Z")}
				p.Commits.Nodes = []github.CommitNode{commitNode("2025-01-02T00:00:00Z", "")}
				return p
			}(),
			author: "alice",
			viewer: "bob",
			bucket: "author_court",
			reason: "reviewed, no new commits",
		},
		{
			name: "draft beats your-own-PR check",
			pr: github.PullRequestNode{
				IsDraft:        true,
				Title:          "feature",
				ReviewDecision: ptr("APPROVED"),
			},
			author: "bob",
			viewer: "bob",
			bucket: "skipped",
			reason: "draft",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bucket, reason := triage(tt.pr, tt.author, tt.viewer)
			if bucket != tt.bucket || reason != tt.reason {
				t.Errorf("got (%q, %q), want (%q, %q)", bucket, reason, tt.bucket, tt.reason)
			}
		})
	}
}

func TestLastReviewerActivity(t *testing.T) {
	tests := []struct {
		name   string
		pr     github.PullRequestNode
		author string
		want   string
	}{
		{
			name:   "empty when no activity",
			pr:     github.PullRequestNode{},
			author: "alice",
			want:   "",
		},
		{
			name: "picks latest of reviews and comments",
			pr: func() github.PullRequestNode {
				p := github.PullRequestNode{}
				p.Reviews.Nodes = []github.ReviewNode{review("carol", "COMMENTED", "2025-01-01T00:00:00Z")}
				p.Comments.Nodes = []github.CommentNode{comment("dave", "2025-01-03T00:00:00Z")}
				return p
			}(),
			author: "alice",
			want:   "2025-01-03T00:00:00Z",
		},
		{
			name: "excludes author's own review",
			pr: func() github.PullRequestNode {
				p := github.PullRequestNode{}
				p.Reviews.Nodes = []github.ReviewNode{
					review("alice", "COMMENTED", "2025-01-05T00:00:00Z"),
					review("carol", "COMMENTED", "2025-01-01T00:00:00Z"),
				}
				return p
			}(),
			author: "alice",
			want:   "2025-01-01T00:00:00Z",
		},
		{
			name: "excludes bots",
			pr: func() github.PullRequestNode {
				p := github.PullRequestNode{}
				p.Reviews.Nodes = []github.ReviewNode{
					review("dependabot[bot]", "COMMENTED", "2025-01-05T00:00:00Z"),
					review("carol", "COMMENTED", "2025-01-01T00:00:00Z"),
				}
				return p
			}(),
			author: "alice",
			want:   "2025-01-01T00:00:00Z",
		},
		{
			name: "handles nil author",
			pr: func() github.PullRequestNode {
				p := github.PullRequestNode{}
				p.Comments.Nodes = []github.CommentNode{
					{Author: nil, CreatedAt: "2025-01-05T00:00:00Z"},
					comment("carol", "2025-01-01T00:00:00Z"),
				}
				return p
			}(),
			author: "alice",
			want:   "2025-01-01T00:00:00Z",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lastReviewerActivity(tt.pr, tt.author)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	t.Run("extracts ticket key from title", func(t *testing.T) {
		pr := github.PullRequestNode{
			Title:  "SLIDE-1234: do the thing",
			Author: &github.Actor{Login: "alice"},
		}
		row := classify(pr, "bob", "slidehq/cloud", "2025-01-01T00:00:00Z")
		if row.TicketKey == nil || *row.TicketKey != "SLIDE-1234" {
			t.Errorf("ticket key = %v, want SLIDE-1234", row.TicketKey)
		}
	})

	t.Run("ticket key is nil when no match", func(t *testing.T) {
		pr := github.PullRequestNode{Title: "no ticket here"}
		row := classify(pr, "bob", "repo", "now")
		if row.TicketKey != nil {
			t.Errorf("ticket key = %v, want nil", *row.TicketKey)
		}
	})

	t.Run("first regex match wins", func(t *testing.T) {
		pr := github.PullRequestNode{Title: "SLIDE-1 and PROJ-99"}
		row := classify(pr, "bob", "repo", "now")
		if row.TicketKey == nil || *row.TicketKey != "SLIDE-1" {
			t.Errorf("ticket key = %v, want SLIDE-1", row.TicketKey)
		}
	})

	t.Run("CI status is lowercased", func(t *testing.T) {
		pr := github.PullRequestNode{Title: "x"}
		pr.Commits.Nodes = []github.CommitNode{commitNode("2025-01-01T00:00:00Z", "SUCCESS")}
		row := classify(pr, "bob", "repo", "now")
		if row.CIStatus == nil || *row.CIStatus != "success" {
			t.Errorf("ci status = %v, want success", row.CIStatus)
		}
		if row.LastCommitAt == nil || *row.LastCommitAt != "2025-01-01T00:00:00Z" {
			t.Errorf("last commit at = %v", row.LastCommitAt)
		}
	})

	t.Run("review status is lowercased", func(t *testing.T) {
		pr := github.PullRequestNode{Title: "x", ReviewDecision: ptr("CHANGES_REQUESTED")}
		row := classify(pr, "bob", "repo", "now")
		if row.ReviewStatus == nil || *row.ReviewStatus != "changes_requested" {
			t.Errorf("review status = %v, want changes_requested", row.ReviewStatus)
		}
	})

	t.Run("approvers excludes author and non-approvers", func(t *testing.T) {
		pr := github.PullRequestNode{
			Title:  "x",
			Author: &github.Actor{Login: "alice"},
		}
		pr.LatestReviews.Nodes = []github.ReviewNode{
			review("alice", "APPROVED", "2025-01-01T00:00:00Z"), // author self-approval, exclude
			review("carol", "APPROVED", "2025-01-02T00:00:00Z"),
			review("dave", "CHANGES_REQUESTED", "2025-01-03T00:00:00Z"), // not approved, exclude
		}
		row := classify(pr, "bob", "repo", "now")
		if row.Approvers == nil || *row.Approvers != `["carol"]` {
			t.Errorf("approvers = %v, want [carol]", row.Approvers)
		}
	})

	t.Run("approvers nil when none", func(t *testing.T) {
		pr := github.PullRequestNode{Title: "x"}
		row := classify(pr, "bob", "repo", "now")
		if row.Approvers != nil {
			t.Errorf("approvers = %v, want nil", *row.Approvers)
		}
	})

	t.Run("engaged_users includes reviewers and commenters", func(t *testing.T) {
		pr := github.PullRequestNode{
			Title:  "x",
			Author: &github.Actor{Login: "alice"},
		}
		pr.Reviews.Nodes = []github.ReviewNode{
			review("carol", "COMMENTED", "2025-01-01T00:00:00Z"),
		}
		pr.Comments.Nodes = []github.CommentNode{
			comment("dave", "2025-01-02T00:00:00Z"),
		}
		row := classify(pr, "bob", "repo", "now")
		if row.EngagedUsers == nil || *row.EngagedUsers != `["carol","dave"]` {
			t.Errorf("engaged_users = %v, want [carol,dave]", row.EngagedUsers)
		}
	})

	t.Run("engaged_users deduplicates across reviews and comments", func(t *testing.T) {
		pr := github.PullRequestNode{
			Title:  "x",
			Author: &github.Actor{Login: "alice"},
		}
		pr.Reviews.Nodes = []github.ReviewNode{
			review("carol", "APPROVED", "2025-01-01T00:00:00Z"),
			review("carol", "COMMENTED", "2025-01-02T00:00:00Z"),
		}
		pr.Comments.Nodes = []github.CommentNode{
			comment("carol", "2025-01-03T00:00:00Z"),
		}
		row := classify(pr, "bob", "repo", "now")
		if row.EngagedUsers == nil || *row.EngagedUsers != `["carol"]` {
			t.Errorf("engaged_users = %v, want [carol]", row.EngagedUsers)
		}
	})

	t.Run("engaged_users excludes author and bots", func(t *testing.T) {
		pr := github.PullRequestNode{
			Title:  "x",
			Author: &github.Actor{Login: "alice"},
		}
		pr.Reviews.Nodes = []github.ReviewNode{
			review("alice", "APPROVED", "2025-01-01T00:00:00Z"),
			review("dependabot[bot]", "COMMENTED", "2025-01-02T00:00:00Z"),
			review("carol", "COMMENTED", "2025-01-03T00:00:00Z"),
		}
		row := classify(pr, "bob", "repo", "now")
		if row.EngagedUsers == nil || *row.EngagedUsers != `["carol"]` {
			t.Errorf("engaged_users = %v, want [carol]", row.EngagedUsers)
		}
	})

	t.Run("engaged_users nil when no engagement", func(t *testing.T) {
		pr := github.PullRequestNode{
			Title:  "x",
			Author: &github.Actor{Login: "alice"},
		}
		row := classify(pr, "bob", "repo", "now")
		if row.EngagedUsers != nil {
			t.Errorf("engaged_users = %v, want nil", *row.EngagedUsers)
		}
	})

	t.Run("author avatar nil when no author", func(t *testing.T) {
		pr := github.PullRequestNode{Title: "x"}
		row := classify(pr, "bob", "repo", "now")
		if row.Author != "" || row.AuthorAvatar != nil {
			t.Errorf("author=%q avatar=%v, want empty/nil", row.Author, row.AuthorAvatar)
		}
	})

	t.Run("author avatar nil when avatar URL empty", func(t *testing.T) {
		pr := github.PullRequestNode{Title: "x", Author: &github.Actor{Login: "alice"}}
		row := classify(pr, "bob", "repo", "now")
		if row.Author != "alice" || row.AuthorAvatar != nil {
			t.Errorf("author=%q avatar=%v, want alice/nil", row.Author, row.AuthorAvatar)
		}
	})

	t.Run("comment count matches nodes", func(t *testing.T) {
		pr := github.PullRequestNode{Title: "x"}
		pr.Comments.Nodes = []github.CommentNode{
			comment("carol", "2025-01-01T00:00:00Z"),
			comment("dave", "2025-01-02T00:00:00Z"),
		}
		row := classify(pr, "bob", "repo", "now")
		if row.CommentCount != 2 {
			t.Errorf("comment count = %d, want 2", row.CommentCount)
		}
	})

	t.Run("populates triage bucket and reason", func(t *testing.T) {
		pr := github.PullRequestNode{
			Title:  "feature",
			Author: &github.Actor{Login: "alice"},
		}
		row := classify(pr, "bob", "repo", "now")
		if row.TriageBucket != "needs_review" {
			t.Errorf("bucket = %q, want needs_review", row.TriageBucket)
		}
		if row.TriageReason == nil || *row.TriageReason != "needs first review" {
			t.Errorf("reason = %v", row.TriageReason)
		}
	})

	t.Run("synced_at and basic fields propagate", func(t *testing.T) {
		pr := github.PullRequestNode{
			Number:    42,
			Title:     "x",
			URL:       "https://example/pr/42",
			Additions: 5,
			Deletions: 3,
			CreatedAt: "2025-01-01T00:00:00Z",
			UpdatedAt: "2025-01-02T00:00:00Z",
		}
		row := classify(pr, "bob", "slidehq/cloud", "2025-01-05T00:00:00Z")
		if row.Number != 42 || row.URL != "https://example/pr/42" ||
			row.Additions != 5 || row.Deletions != 3 ||
			row.Repo != "slidehq/cloud" || row.SyncedAt != "2025-01-05T00:00:00Z" ||
			row.CreatedAt != "2025-01-01T00:00:00Z" || row.UpdatedAt != "2025-01-02T00:00:00Z" {
			t.Errorf("got %+v", row)
		}
	})
}

func TestIsBot(t *testing.T) {
	tests := map[string]bool{
		"dependabot[bot]":     true,
		"renovate[bot]":       true,
		"alice":               false,
		"":                    false,
		"weird[bot]name":      false,
		"name-with-[bot]-mid": false,
	}
	for in, want := range tests {
		if got := isBot(in); got != want {
			t.Errorf("isBot(%q) = %v, want %v", in, got, want)
		}
	}
}
