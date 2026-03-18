# reviews

Local PR review dashboard. Open PRs across your GitHub repos, grouped by Jira ticket, sorted by last activity.

Runs on your machine, uses your GitHub PAT, stores state in local SQLite.

## Setup

Create a **fine-grained personal access token** at https://github.com/settings/personal-access-tokens/new

Grant access to the repos you want to track, then enable these **repository permissions** (read-only):

| Permission | Why |
|---|---|
| **Pull requests** | PRs, reviews, review comments |
| **Contents** | Commit timestamps |
| **Commit statuses** | CI pass/fail |
| **Metadata** | Repo discovery (auto-included) |

```
export GITHUB_TOKEN=github_pat_...
make build
./reviews
```

Open http://localhost:8080

## What it does

- Fetches open PRs from all repos your token can access
- Groups by Jira ticket key (extracted from PR title)
- Sorts by last activity
- Filters: hide drafts, hide approved, hide failing CI, repo filter
- Auto-syncs every 10 minutes

## Usage

```
./reviews                     # start dashboard on :8080
./reviews --port 3000         # custom port
./reviews sync                # one-shot sync, print summary
./reviews sync --verbose      # detailed sync output
```

## Requirements

- Go 1.22+
- GitHub fine-grained personal access token (see Setup)
