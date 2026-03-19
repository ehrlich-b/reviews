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

func (s *Store) AddTeamMember(username string) error {
	_, err := s.db.Exec(`INSERT INTO team_members (username) VALUES (?)
		ON CONFLICT(username) DO NOTHING`, username)
	if err != nil {
		return fmt.Errorf("add team member: %w", err)
	}
	return nil
}

func (s *Store) RemoveTeamMember(username string) error {
	_, err := s.db.Exec("DELETE FROM team_members WHERE username = ?", username)
	if err != nil {
		return fmt.Errorf("remove team member: %w", err)
	}
	return nil
}

func (s *Store) ListTeamMembers() ([]string, error) {
	rows, err := s.db.Query("SELECT username FROM team_members ORDER BY username")
	if err != nil {
		return nil, fmt.Errorf("list team members: %w", err)
	}
	defer rows.Close()

	var members []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("scan team member: %w", err)
		}
		members = append(members, u)
	}
	return members, nil
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
