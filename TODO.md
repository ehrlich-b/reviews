# reviews — TODO

## Milestone 1: Skeleton

- [x] `go mod init github.com/ehrlich-b/reviews`
- [x] Makefile (build, test, run, clean)
- [x] `cmd/reviews/main.go` — entry point, serve + sync dispatch
- [x] `internal/db/db.go` — Open(), WAL mode, foreign keys, migration runner
- [x] `internal/db/migrations/001_init.sql` — pull_requests, sync_state tables

## Milestone 2: GitHub Sync

- [x] `internal/github/client.go` — GraphQL client with PAT auth (`Authorization: bearer`)
- [x] `internal/github/query.go` — PR query with fragment (reviews, comments, commits, CI, author)
- [x] Repo batching via GraphQL aliases (~5 repos per query)
- [x] `viewer { login }` for current user detection
- [x] Repo discovery via `viewer { repositories }` filtered to repos with open PRs
- [x] `internal/sync/sync.go` — orchestrate: discover repos, batch-fetch PRs, run triage, upsert into SQLite
- [x] Triage classifier: implement the commits-since-review heuristic (needs_review / author_court / approved / skipped + reason string)
- [x] Ticket key extraction from PR title (regex `[A-Z]+-\d+`, first match)
- [x] Prune closed/merged PRs (delete rows absent from sync response)
- [x] `reviews sync` CLI command with summary output (N needs review, N author's court, N approved, N skipped)

## Milestone 3: Web Dashboard

- [x] `internal/server/server.go` — HTTP server, template setup, routes
- [x] `internal/server/templates/base.html` — layout, dark/light theme (system preference + toggle)
- [x] `internal/server/templates/queue.html` — main dashboard
- [x] "Needs Your Review" section: PRs where triage_bucket = needs_review, grouped by ticket_key, ordered by most recent updated_at within group
- [x] Ticket group headers showing ticket key, PR count, repos touched, last activity
- [x] "Your PRs" section: PRs where author = current user, grouped by status (approved / changes requested / waiting)
- [x] "ungrouped" bucket for PRs with no ticket key
- [x] Triage reason displayed per PR row (human-readable)
- [x] Filter controls: hide failing CI, hide approved, hide drafts, repo filter
- [x] Filter state persisted in localStorage
- [x] Relative timestamps (30m ago, 2h ago, 1d ago)
- [x] Each PR row links to GitHub

## Milestone 4: Auto-Sync

- [x] Background sync goroutine (every 10 min)
- [x] `POST /api/sync` endpoint (manual refresh)
- [x] Refresh button in UI
- [x] Sync status in footer (last synced X ago, N PRs across M repos)

## Future

- [ ] Review cache with SHA tracking (detect what changed between sessions)
- [ ] Jira integration (epic names for richer group labels)
- [ ] PR detail view (diff stats, file list, review timeline)
- [ ] Semantic diff classification
- [ ] Incremental review state tracking
- [ ] AI pre-annotation layer
