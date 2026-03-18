# reviews — Design Document

## What This Is

A local PR review dashboard. Single Go binary, runs on localhost, reads GitHub via your PAT, caches in SQLite. Shows you what needs review, grouped by Jira ticket, ordered by last activity.

The problem: 10 things in flight, PRs posted in a reviews channel, no way to see the big picture or know what needs your attention right now. GitHub's notification system is noise. This is signal.

## V0: The Queue

A single-page dashboard showing open PRs across all repos your PAT can see, classified by what actually needs your attention.

### Triage Classification

Every open PR gets classified into one of four buckets. The core heuristic is **"commits since last non-author human review activity"** — borrowed from an existing CLI-based triage system that's been battle-tested against 7 repos and 30-40 open PRs.

**NEEDS REVIEW** — actually actionable right now:
- Nobody reviewed yet (needs first review)
- Changes were requested AND author pushed new commits (needs re-review)
- Someone reviewed AND author pushed new commits since (needs re-review)

**AUTHOR'S COURT** — reviewed, nothing new from author:
- Changes requested, no new commits since feedback
- Reviewed, no new commits since review
- Ball is in the author's court. Don't look at these.

**APPROVED** — ready to merge:
- Has an approving review. Merge yours, nudge others.

**SKIPPED** — filtered out:
- Drafts
- WIP titles (case-insensitive `^WIP`)
- Your own PRs (unless approved — then shown as reminder to merge)

This replaces a simplistic "hide waiting on author" toggle with real signal.

### Grouping

PRs are grouped by Jira ticket key, extracted from the PR title via regex (`[A-Z]+-\d+`). First match wins. Multiple PRs for the same ticket appear together — that's how you see the pieces of a feature across repos. A ticket touching `common`, `cloud`, and `console` shows all three PRs in one group.

Groups are sorted by the most recent activity across their PRs.

PRs with no ticket key in the title land in an "ungrouped" bucket at the bottom.

### Sections

The dashboard has two top-level sections:

**Needs Your Review** — PRs classified as NEEDS REVIEW, grouped by ticket, ordered by last activity. This is the main view. Everything you should be looking at.

**Your PRs** — PRs where you are the author. Subsections:
- Approved (merge these)
- Changes requested / threads needing response (address these)
- Waiting on review (nothing to do)

### Filters

Toggle via UI controls, persist in localStorage:

- **Hide failing CI** — Default: off. CI failures from cross-repo dependencies are expected and normal. This is intentionally NOT part of triage classification — it's informational only.
- **Hide approved** — Default: off. Useful to focus on what still needs work.
- **Hide drafts** — Default: on.
- **Repo filter** — Show/hide specific repos.

### PR Row

Each PR row shows:
- Repo name (short, e.g., `cloud`)
- PR number + title (linked to GitHub)
- Author (avatar + username)
- Triage bucket indicator (needs review / author's court / approved)
- CI status: passing / failing / pending (informational, not triage)
- Time since last update (relative)
- Comment count
- Triage reason (short text: "needs first review", "new commits since your review", "changes requested, no new commits", etc.)

### What "Last Modified" Means

`updated_at` from the GitHub API. Captures pushes, comments, reviews, label changes — any activity. Good enough for ordering.

## Data Model

SQLite at `~/.reviews/reviews.db`.

### pull_requests

One row per open PR. Deleted when a PR is closed/merged (absent from sync response).

```sql
CREATE TABLE pull_requests (
    id INTEGER PRIMARY KEY,
    repo TEXT NOT NULL,              -- "org/repo"
    number INTEGER NOT NULL,
    title TEXT NOT NULL,
    author TEXT NOT NULL,
    author_avatar TEXT,
    url TEXT NOT NULL,
    draft INTEGER NOT NULL DEFAULT 0,
    comment_count INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,        -- ISO 8601

    -- Derived at sync time
    ticket_key TEXT,                 -- "SLIDE-1234" or NULL
    ci_status TEXT,                  -- "success" / "failure" / "pending" / NULL
    review_status TEXT,              -- "approved" / "changes_requested" / "review_required" / NULL
    triage_bucket TEXT NOT NULL,     -- "needs_review" / "author_court" / "approved" / "skipped"
    triage_reason TEXT,              -- human-readable: "needs first review", "new commits since review", etc.
    last_commit_at TEXT,             -- ISO 8601, latest commit timestamp
    last_review_activity_at TEXT,    -- ISO 8601, latest non-author human comment/review
    approvers TEXT,                  -- JSON array of usernames

    synced_at TEXT NOT NULL,
    UNIQUE(repo, number)
);
```

### sync_state

Tracks when each repo was last synced.

```sql
CREATE TABLE sync_state (
    repo TEXT PRIMARY KEY,
    last_sync TEXT NOT NULL,         -- ISO 8601
    etag TEXT                        -- GitHub conditional request
);
```

## Triage Algorithm

Run at sync time for each PR. All timestamps compared as ISO 8601 strings.

```
input: PR with reviews[], comments[], commits[], author, reviewDecision

1. Skip if draft or title matches ^WIP (case-insensitive)
2. Skip if author == current user AND not approved

3. If reviewDecision == "APPROVED" → bucket: approved

4. last_commit = max(commits[].committedDate)
   last_reviewer_activity = max(
     reviews[].submittedAt where author != PR.author,
     comments[].createdAt where author != PR.author and not bot
   )

5. has_reviews = any non-author human reviews exist
   changes_requested = any latestReview[].state == "CHANGES_REQUESTED" from non-author
   new_commits = last_commit > last_reviewer_activity

6. If no reviews → bucket: needs_review, reason: "needs first review"
   If changes_requested AND new_commits → needs_review, "author pushed fixes — re-review"
   If changes_requested AND NOT new_commits → author_court, "changes requested, no new commits"
   If has_reviews AND new_commits → needs_review, "new commits since review"
   If has_reviews AND NOT new_commits → author_court, "reviewed, no new commits"
```

This is the same heuristic as `pr-triage.sh` from the existing CLI tooling, translated into Go and stored in SQLite.

## Sync Model

GitHub GraphQL API with PAT auth (`Authorization: bearer xxx`). One query per repo gets all open PRs with full triage data. Batch repos with aliases to minimize round trips.

### The Query

One query per repo fetches everything needed for triage:

```graphql
query($owner: String!, $repo: String!) {
  repository(owner: $owner, name: $repo) {
    pullRequests(states: OPEN, first: 50, orderBy: {field: UPDATED_AT, direction: DESC}) {
      nodes {
        number
        title
        url
        isDraft
        updatedAt
        author { login avatarUrl }
        reviewDecision
        commits(last: 1) {
          nodes {
            commit {
              committedDate
              statusCheckRollup { state }
            }
          }
        }
        latestReviews(first: 10) {
          nodes { author { login } state submittedAt }
        }
        reviews(first: 50) {
          nodes { author { login } state submittedAt }
        }
        comments(last: 20) {
          nodes { author { login } createdAt }
        }
      }
    }
  }
}
```

Batch multiple repos in a single request using aliases:

```graphql
query {
  cloud: repository(owner: "slidehq", name: "cloud") { ...prFields }
  console: repository(owner: "slidehq", name: "console") { ...prFields }
  agent: repository(owner: "slidehq", name: "agent") { ...prFields }
}
```

20 repos = ~4-5 API calls (batching ~5 repos per query to stay under GraphQL complexity limits). Triage classification runs on the response, results stored in SQLite.

### When sync happens

- On server start (blocking)
- Every 10 minutes in background
- Manual trigger via `POST /api/sync` (refresh button in UI)

### Rate limits

GraphQL rate limit: 5000 points/hr. Each query costs ~1 point per node requested. A full sync across 20 repos with 50 PRs is a handful of queries — effectively free.

### Repo discovery

On first sync, use GraphQL `viewer { repositories }` to discover all repos the PAT can access. Filter to those with open PRs. Re-discover on each sync cycle.

### Current user detection

GraphQL `viewer { login }` — comes free in any query. Needed for "your PRs" section and to exclude your own PRs from the review queue (unless approved).

## UI

Server-rendered HTML. Single page.

### Layout

```
+----------------------------------------------------------+
| reviews                           [Refresh] [Filters v]  |
+----------------------------------------------------------+
|                                                            |
| NEEDS YOUR REVIEW (5)                                     |
|                                                            |
| SLIDE-1234 - 3 PRs across cloud, console     last: 30m   |
| +--------------------------------------------------------+|
| | cloud #456   Add backup job model          @jdale      ||
| | needs first review  CI pass  30m ago  0 comments       ||
| |--------------------------------------------------------||
| | cloud #461   Wire up cron trigger          @ckosie     ||
| | author pushed fixes  CI fail  1h ago  5 comments       ||
| |--------------------------------------------------------||
| | console #89  Backup scheduling UI          @iallheim   ||
| | new commits since review  CI pass  2h ago  2 comments  ||
| +--------------------------------------------------------+|
|                                                            |
| SLIDE-1235 - 1 PR                             last: 4h   |
| +--------------------------------------------------------+|
| | cloud #460  Increase sync timeout          @iallheim   ||
| | needs first review  CI pass  4h ago  1 comment         ||
| +--------------------------------------------------------+|
|                                                            |
| ungrouped - 1 PR                               last: 1d  |
| +--------------------------------------------------------+|
| | cloud #462  Fix typo in README             @jdale      ||
| | needs first review  CI pass  1d ago  0 comments        ||
| +--------------------------------------------------------+|
|                                                            |
|------------------------------------------------------------
|                                                            |
| YOUR PRS (3)                                              |
|                                                            |
| cloud #455  SLIDE-1230 Refactor auth middleware            |
| approved — merge it                                       |
|                                                            |
| cloud #450  SLIDE-1228 Add rate limiting                  |
| changes requested, no new commits                         |
|                                                            |
| console #85  SLIDE-1229 Settings page                     |
| waiting on review                                         |
|                                                            |
+----------------------------------------------------------+
| synced 2m ago | 52 open PRs across 8 repos                |
+----------------------------------------------------------+
```

### Style

Minimal. Dark/light theme (respect system preference, toggle in UI). No CSS framework. Monospace for PR numbers and timestamps, proportional for titles/names.

## CLI

```
reviews                     # start server on localhost:8080
reviews sync                # one-shot sync, print summary, exit
reviews sync --verbose      # per-repo detail
```

## Technical Stack

- Go 1.22+, `http.ServeMux`, `html/template`
- modernc.org/sqlite, WAL mode, foreign keys, embedded migrations
- GitHub GraphQL API (batched queries, ~5 calls per full sync)
- No npm, no framework, no build step
- Templates embedded via `//go:embed templates/*.html`
- Migrations embedded via `//go:embed migrations/*.sql`
- `GITHUB_TOKEN` env var for PAT
- Default port 8080 (`--port`), DB at `~/.reviews/reviews.db` (`--db`)

## Future (not v0)

Acknowledged from the brainstorm, not designed yet:

- Semantic diff classification (tree-sitter AST diffing)
- Incremental review state (track what you've read per-file per-commit)
- PR viewer (better diff UI, keyboard navigation)
- AI pre-annotation (optional Claude layer for summaries)
- Served mode (team-wide deployment, multi-user)
- Jira integration (epic hierarchy for richer grouping)
- Review cache with SHA tracking (detect what changed between sessions)
