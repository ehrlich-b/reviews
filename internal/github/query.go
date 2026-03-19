package github

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

const prFieldsFragment = `
fragment prFields on Repository {
	pullRequests(states: OPEN, first: 50, orderBy: {field: UPDATED_AT, direction: DESC}) {
		nodes {
			number
			title
			url
			isDraft
			updatedAt
			additions
			deletions
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
`

type Actor struct {
	Login     string `json:"login"`
	AvatarUrl string `json:"avatarUrl"`
}

type CommitNode struct {
	Commit struct {
		CommittedDate     string `json:"committedDate"`
		StatusCheckRollup *struct {
			State string `json:"state"`
		} `json:"statusCheckRollup"`
	} `json:"commit"`
}

type ReviewNode struct {
	Author      *Actor `json:"author"`
	State       string `json:"state"`
	SubmittedAt string `json:"submittedAt"`
}

type CommentNode struct {
	Author    *Actor `json:"author"`
	CreatedAt string `json:"createdAt"`
}

type PullRequestNode struct {
	Number         int     `json:"number"`
	Title          string  `json:"title"`
	URL            string  `json:"url"`
	IsDraft        bool    `json:"isDraft"`
	UpdatedAt      string  `json:"updatedAt"`
	Additions      int     `json:"additions"`
	Deletions      int     `json:"deletions"`
	Author         *Actor  `json:"author"`
	ReviewDecision *string `json:"reviewDecision"`
	Commits        struct {
		Nodes []CommitNode `json:"nodes"`
	} `json:"commits"`
	LatestReviews struct {
		Nodes []ReviewNode `json:"nodes"`
	} `json:"latestReviews"`
	Reviews struct {
		Nodes []ReviewNode `json:"nodes"`
	} `json:"reviews"`
	Comments struct {
		Nodes []CommentNode `json:"nodes"`
	} `json:"comments"`
}

type repositoryResult struct {
	PullRequests struct {
		Nodes []PullRequestNode `json:"nodes"`
	} `json:"pullRequests"`
}

const batchSize = 5

// FetchRepoPRs fetches open PRs for all given repos, batching into groups of 5.
func (c *Client) FetchRepoPRs(repos []string) (map[string][]PullRequestNode, error) {
	result := make(map[string][]PullRequestNode)

	for i := 0; i < len(repos); i += batchSize {
		end := i + batchSize
		if end > len(repos) {
			end = len(repos)
		}
		batch := repos[i:end]

		batchResult, err := c.fetchBatch(batch)
		if err != nil {
			return nil, err
		}
		for k, v := range batchResult {
			result[k] = v
		}
	}
	return result, nil
}

func (c *Client) fetchBatch(repos []string) (map[string][]PullRequestNode, error) {
	aliases := make(map[string]string)
	var parts []string

	for i, repo := range repos {
		owner, name, ok := splitRepo(repo)
		if !ok {
			return nil, fmt.Errorf("invalid repo format: %s", repo)
		}
		alias := fmt.Sprintf("repo%d", i)
		aliases[alias] = repo
		parts = append(parts, fmt.Sprintf("  %s: repository(owner: %q, name: %q) { ...prFields }", alias, owner, name))
	}

	query := fmt.Sprintf("{\n%s\n}\n%s", strings.Join(parts, "\n"), prFieldsFragment)

	var raw map[string]json.RawMessage
	if err := c.do(query, nil, &raw); err != nil {
		return nil, fmt.Errorf("fetch PRs: %w", err)
	}

	result := make(map[string][]PullRequestNode)
	for alias, data := range raw {
		fullName, ok := aliases[alias]
		if !ok {
			continue
		}
		if string(data) == "null" {
			log.Printf("repo %s not accessible, skipping", fullName)
			continue
		}
		var repo repositoryResult
		if err := json.Unmarshal(data, &repo); err != nil {
			log.Printf("decode repo %s: %v, skipping", fullName, err)
			continue
		}
		result[fullName] = repo.PullRequests.Nodes
	}
	return result, nil
}

func splitRepo(full string) (owner, name string, ok bool) {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}
