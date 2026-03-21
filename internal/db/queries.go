package db

import (
	"database/sql"
	"fmt"
	"strings"
)

type PullRequest struct {
	ID                   int64
	Repo                 string
	Number               int
	Title                string
	Author               string
	AuthorAvatar         *string
	URL                  string
	Draft                bool
	CommentCount         int
	UpdatedAt            string
	TicketKey            *string
	CIStatus             *string
	ReviewStatus         *string
	TriageBucket         string
	TriageReason         *string
	LastCommitAt         *string
	LastReviewActivityAt *string
	Approvers            *string
	Additions            int
	Deletions            int
	SyncedAt             string
}

func (s *Store) UpsertPR(pr *PullRequest) error {
	_, err := s.db.Exec(`INSERT INTO pull_requests
		(repo, number, title, author, author_avatar, url, draft, comment_count, updated_at,
		 ticket_key, ci_status, review_status, triage_bucket, triage_reason,
		 last_commit_at, last_review_activity_at, approvers, additions, deletions, synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo, number) DO UPDATE SET
			title=excluded.title, author=excluded.author, author_avatar=excluded.author_avatar,
			url=excluded.url, draft=excluded.draft, comment_count=excluded.comment_count,
			updated_at=excluded.updated_at, ticket_key=excluded.ticket_key,
			ci_status=excluded.ci_status, review_status=excluded.review_status,
			triage_bucket=excluded.triage_bucket, triage_reason=excluded.triage_reason,
			last_commit_at=excluded.last_commit_at, last_review_activity_at=excluded.last_review_activity_at,
			approvers=excluded.approvers, additions=excluded.additions, deletions=excluded.deletions,
			synced_at=excluded.synced_at`,
		pr.Repo, pr.Number, pr.Title, pr.Author, pr.AuthorAvatar, pr.URL,
		pr.Draft, pr.CommentCount, pr.UpdatedAt,
		pr.TicketKey, pr.CIStatus, pr.ReviewStatus, pr.TriageBucket, pr.TriageReason,
		pr.LastCommitAt, pr.LastReviewActivityAt, pr.Approvers, pr.Additions, pr.Deletions, pr.SyncedAt)
	if err != nil {
		return fmt.Errorf("upsert PR: %w", err)
	}
	return nil
}

func (s *Store) PrunePRs(repo string, openNumbers []int) error {
	if len(openNumbers) == 0 {
		_, err := s.db.Exec("DELETE FROM pull_requests WHERE repo = ?", repo)
		if err != nil {
			return fmt.Errorf("prune all PRs for %s: %w", repo, err)
		}
		return nil
	}

	placeholders := make([]string, len(openNumbers))
	args := []any{repo}
	for i, n := range openNumbers {
		placeholders[i] = "?"
		args = append(args, n)
	}
	query := fmt.Sprintf("DELETE FROM pull_requests WHERE repo = ? AND number NOT IN (%s)",
		strings.Join(placeholders, ","))
	_, err := s.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("prune PRs for %s: %w", repo, err)
	}
	return nil
}

func (s *Store) ListSyncedRepos() ([]string, error) {
	rows, err := s.db.Query("SELECT repo FROM sync_state")
	if err != nil {
		return nil, fmt.Errorf("list synced repos: %w", err)
	}
	defer rows.Close()
	var repos []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, fmt.Errorf("scan synced repo: %w", err)
		}
		repos = append(repos, r)
	}
	return repos, rows.Err()
}

func (s *Store) DeleteSyncState(repo string) error {
	_, err := s.db.Exec("DELETE FROM sync_state WHERE repo = ?", repo)
	if err != nil {
		return fmt.Errorf("delete sync state %s: %w", repo, err)
	}
	return nil
}

func (s *Store) SetSyncState(repo, syncedAt string) error {
	_, err := s.db.Exec(`INSERT INTO sync_state (repo, last_sync) VALUES (?, ?)
		ON CONFLICT(repo) DO UPDATE SET last_sync=excluded.last_sync`,
		repo, syncedAt)
	if err != nil {
		return fmt.Errorf("set sync state: %w", err)
	}
	return nil
}

func (s *Store) SetConfig(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO config (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("set config %s: %w", key, err)
	}
	return nil
}

func (s *Store) GetConfig(key string) (string, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM config WHERE key = ?", key).Scan(&value)
	if err != nil {
		return "", err
	}
	return value, nil
}

func (s *Store) ListPRs() ([]*PullRequest, error) {
	rows, err := s.db.Query(`SELECT id, repo, number, title, author, author_avatar, url, draft,
		comment_count, updated_at, ticket_key, ci_status, review_status, triage_bucket,
		triage_reason, last_commit_at, last_review_activity_at, approvers, additions, deletions, synced_at
		FROM pull_requests ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list PRs: %w", err)
	}
	defer rows.Close()

	var prs []*PullRequest
	for rows.Next() {
		pr := &PullRequest{}
		err := rows.Scan(&pr.ID, &pr.Repo, &pr.Number, &pr.Title, &pr.Author,
			&pr.AuthorAvatar, &pr.URL, &pr.Draft, &pr.CommentCount, &pr.UpdatedAt,
			&pr.TicketKey, &pr.CIStatus, &pr.ReviewStatus, &pr.TriageBucket,
			&pr.TriageReason, &pr.LastCommitAt, &pr.LastReviewActivityAt,
			&pr.Approvers, &pr.Additions, &pr.Deletions, &pr.SyncedAt)
		if err != nil {
			return nil, fmt.Errorf("scan PR: %w", err)
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

// Named teams

func (s *Store) CreateTeam(name string) error {
	_, err := s.db.Exec(`INSERT INTO teams (name) VALUES (?) ON CONFLICT(name) DO NOTHING`, name)
	if err != nil {
		return fmt.Errorf("create team: %w", err)
	}
	return nil
}

func (s *Store) DeleteTeam(name string) error {
	_, err := s.db.Exec("DELETE FROM teams WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("delete team: %w", err)
	}
	return nil
}

func (s *Store) ListTeams() ([]string, error) {
	rows, err := s.db.Query("SELECT name FROM teams ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	defer rows.Close()
	var teams []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("scan team: %w", err)
		}
		teams = append(teams, t)
	}
	return teams, rows.Err()
}

func (s *Store) AddTeamMembership(team, username string) error {
	_, err := s.db.Exec(`INSERT INTO team_memberships (team_name, username) VALUES (?, ?)
		ON CONFLICT(team_name, username) DO NOTHING`, team, username)
	if err != nil {
		return fmt.Errorf("add team membership: %w", err)
	}
	return nil
}

func (s *Store) RemoveTeamMembership(team, username string) error {
	_, err := s.db.Exec("DELETE FROM team_memberships WHERE team_name = ? AND username = ?", team, username)
	if err != nil {
		return fmt.Errorf("remove team membership: %w", err)
	}
	return nil
}

func (s *Store) ListTeamMemberships() (map[string][]string, error) {
	rows, err := s.db.Query("SELECT team_name, username FROM team_memberships ORDER BY team_name, username")
	if err != nil {
		return nil, fmt.Errorf("list team memberships: %w", err)
	}
	defer rows.Close()
	result := map[string][]string{}
	for rows.Next() {
		var team, user string
		if err := rows.Scan(&team, &user); err != nil {
			return nil, fmt.Errorf("scan membership: %w", err)
		}
		result[team] = append(result[team], user)
	}
	return result, rows.Err()
}

// Slack mappings

type SlackMapping struct {
	GithubUsername string
	SlackUserID   string
	Timezone      string
}

func (s *Store) SetSlackMapping(githubUsername, slackUserID, timezone string) error {
	_, err := s.db.Exec(`INSERT INTO slack_mappings (github_username, slack_user_id, timezone) VALUES (?, ?, ?)
		ON CONFLICT(github_username) DO UPDATE SET slack_user_id=excluded.slack_user_id, timezone=excluded.timezone`,
		githubUsername, slackUserID, timezone)
	if err != nil {
		return fmt.Errorf("set slack mapping: %w", err)
	}
	return nil
}

func (s *Store) RemoveSlackMapping(githubUsername string) error {
	_, err := s.db.Exec("DELETE FROM slack_mappings WHERE github_username = ?", githubUsername)
	if err != nil {
		return fmt.Errorf("remove slack mapping: %w", err)
	}
	return nil
}

func (s *Store) ListSlackMappings() ([]SlackMapping, error) {
	rows, err := s.db.Query("SELECT github_username, slack_user_id, timezone FROM slack_mappings ORDER BY github_username")
	if err != nil {
		return nil, fmt.Errorf("list slack mappings: %w", err)
	}
	defer rows.Close()
	var mappings []SlackMapping
	for rows.Next() {
		var m SlackMapping
		if err := rows.Scan(&m.GithubUsername, &m.SlackUserID, &m.Timezone); err != nil {
			return nil, fmt.Errorf("scan slack mapping: %w", err)
		}
		mappings = append(mappings, m)
	}
	return mappings, rows.Err()
}

// Nag log

func (s *Store) GetLastNag(prKey string) (string, error) {
	var naggedAt string
	err := s.db.QueryRow("SELECT nagged_at FROM nag_log WHERE pr_key = ?", prKey).Scan(&naggedAt)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get last nag: %w", err)
	}
	return naggedAt, nil
}

func (s *Store) SetLastNag(prKey, naggedAt string) error {
	_, err := s.db.Exec(`INSERT INTO nag_log (pr_key, nagged_at) VALUES (?, ?)
		ON CONFLICT(pr_key) DO UPDATE SET nagged_at=excluded.nagged_at`, prKey, naggedAt)
	if err != nil {
		return fmt.Errorf("set last nag: %w", err)
	}
	return nil
}

func (s *Store) PruneNagLog() error {
	_, err := s.db.Exec(`DELETE FROM nag_log WHERE pr_key NOT IN
		(SELECT repo || '#' || number FROM pull_requests)`)
	if err != nil {
		return fmt.Errorf("prune nag log: %w", err)
	}
	return nil
}

// Jira issues

type JiraIssue struct {
	Key         string
	Summary     string
	Status      string
	EpicKey     *string
	EpicSummary *string
}

func (s *Store) UpsertJiraIssue(key, summary, status string, epicKey, epicSummary *string, syncedAt string) error {
	_, err := s.db.Exec(`INSERT INTO jira_issues (key, summary, status, epic_key, epic_summary, synced_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET summary=excluded.summary, status=excluded.status,
		epic_key=excluded.epic_key, epic_summary=excluded.epic_summary, synced_at=excluded.synced_at`,
		key, summary, status, epicKey, epicSummary, syncedAt)
	if err != nil {
		return fmt.Errorf("upsert jira issue: %w", err)
	}
	return nil
}

func (s *Store) GetJiraIssues(keys []string) (map[string]*JiraIssue, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, k := range keys {
		placeholders[i] = "?"
		args[i] = k
	}
	query := fmt.Sprintf("SELECT key, summary, status, epic_key, epic_summary FROM jira_issues WHERE key IN (%s)",
		strings.Join(placeholders, ","))
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get jira issues: %w", err)
	}
	defer rows.Close()
	result := map[string]*JiraIssue{}
	for rows.Next() {
		var j JiraIssue
		if err := rows.Scan(&j.Key, &j.Summary, &j.Status, &j.EpicKey, &j.EpicSummary); err != nil {
			return nil, fmt.Errorf("scan jira issue: %w", err)
		}
		result[j.Key] = &j
	}
	return result, rows.Err()
}

func (s *Store) GetSyncInfo() (repoCount int, prCount int, lastSync string, err error) {
	err = s.db.QueryRow("SELECT COUNT(*) FROM sync_state").Scan(&repoCount)
	if err != nil {
		return 0, 0, "", fmt.Errorf("count repos: %w", err)
	}
	err = s.db.QueryRow("SELECT COUNT(*) FROM pull_requests").Scan(&prCount)
	if err != nil {
		return 0, 0, "", fmt.Errorf("count PRs: %w", err)
	}
	var ls sql.NullString
	err = s.db.QueryRow("SELECT MAX(last_sync) FROM sync_state").Scan(&ls)
	if err != nil {
		return 0, 0, "", fmt.Errorf("get last sync: %w", err)
	}
	if ls.Valid {
		lastSync = ls.String
	}
	return repoCount, prCount, lastSync, nil
}
