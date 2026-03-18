# CLAUDE.md — reviews

## Project Overview

A local PR review dashboard. Runs on localhost, reads GitHub via PAT, caches in SQLite. Shows open PRs classified by triage state, grouped by Jira ticket key, split into "needs your review" and "your PRs." See DESIGN.md for the full spec including the triage algorithm.

## Tech Stack

Same conventions as ~/repos/read:

- **Single binary Go + SQLite**. No Docker, no microservices, no ORM.
- **modernc.org/sqlite** (pure Go, no CGO). WAL mode + foreign keys enabled on open.
- **Embedded migrations** via `//go:embed migrations/*.sql`. Tracked in `schema_migrations` table. Auto-run on DB open.
- **Go 1.22+ http.ServeMux** routing (`"GET /path"`). No framework.
- **Server-rendered HTML** via Go templates. No React, no npm, no node_modules.
- **stdlib `log`** only. No logging library.
- **`fmt.Errorf("context: %w", err)`** for all error wrapping. No panics.
- **No ORM**. Raw `sql.Query`/`QueryRow` with `Scan`.
- **Pointer fields** for nullable columns (`*string`, `*time.Time`).

## Project Structure

```
reviews/
├── CLAUDE.md
├── DESIGN.md
├── TODO.md
├── README.md
├── Makefile
├── go.mod / go.sum
├── cmd/reviews/main.go          CLI entry point (serve + sync)
├── internal/
│   ├── db/                      SQLite open, migrations, query methods
│   │   └── migrations/          Numbered .sql files (001_init.sql, ...)
│   ├── github/                  GraphQL client, PR query, repo discovery
│   ├── sync/                    Sync orchestration + triage classification
│   └── server/                  HTTP handlers, template rendering
│       └── templates/           Embedded HTML templates (base.html, queue.html)
```

## Environment

- `GITHUB_TOKEN` — GitHub fine-grained PAT with Pull requests, Contents, and Commit statuses read access (required)

## Development

```
make build          # go build -o reviews ./cmd/reviews
make test           # go test ./...
make run            # build + ./reviews
```

## GitHub API

- **GraphQL API** (`POST https://api.github.com/graphql`)
- Auth: `Authorization: bearer $GITHUB_TOKEN`
- One query per repo gets all open PRs with reviews, comments, commits, CI status
- Batch repos with GraphQL aliases (~5 per query) to minimize round trips
- `viewer { login }` for current user detection
- Sync every 10 minutes in background, manual refresh via UI
- Rate limit: 5000 points/hr — a full sync is a handful of queries

## Triage Heuristic

Core signal: **commits since last non-author human review activity**. This is the same heuristic used in the existing `pr-triage.sh` CLI tool across Slide repos.

- New commits since last review → needs re-review
- Reviewed but no new commits → author's court (ignore)
- Nobody reviewed → needs first review
- CI status is intentionally NOT a triage signal (cross-repo deps cause expected failures)

See DESIGN.md "Triage Algorithm" section for the full pseudocode.

## Ticket Key Extraction

Jira ticket keys extracted from PR titles via regex. Default pattern: `[A-Z]+-\d+`. First match wins. No match = "ungrouped" bucket.
