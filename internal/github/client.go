package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

const graphQLEndpoint = "https://api.github.com/graphql"

type Client struct {
	token      string
	httpClient *http.Client
}

func NewClient(token string) *Client {
	return &Client{
		token:      token,
		httpClient: &http.Client{},
	}
}

type graphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type graphQLError struct {
	Message string `json:"message"`
}

func (c *Client) do(query string, variables map[string]any, result any) error {
	body, err := json.Marshal(graphQLRequest{Query: query, Variables: variables})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", graphQLEndpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != 200 {
		return fmt.Errorf("github api: %d %s", resp.StatusCode, string(respBody))
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphQLError  `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if len(envelope.Errors) > 0 && len(envelope.Data) == 0 {
		return fmt.Errorf("graphql: %s", envelope.Errors[0].Message)
	}
	if result != nil {
		if err := json.Unmarshal(envelope.Data, result); err != nil {
			return fmt.Errorf("decode data: %w", err)
		}
	}
	return nil
}

func (c *Client) ViewerLogin() (string, error) {
	var result struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
	}
	err := c.do("{ viewer { login } }", nil, &result)
	if err != nil {
		return "", fmt.Errorf("viewer login: %w", err)
	}
	return result.Viewer.Login, nil
}

func (c *Client) DiscoverRepos(orgs []string) ([]string, error) {
	seen := map[string]bool{}
	var repos []string

	// Only scan viewer's personal repos when no org is configured
	if len(orgs) == 0 {
		var cursor *string
		for {
			var result struct {
				Viewer struct {
					Repositories struct {
						Nodes []struct {
							NameWithOwner string `json:"nameWithOwner"`
							PullRequests  struct {
								TotalCount int `json:"totalCount"`
							} `json:"pullRequests"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"repositories"`
				} `json:"viewer"`
			}

			after := ""
			if cursor != nil {
				after = fmt.Sprintf(`, after: %q`, *cursor)
			}
			query := fmt.Sprintf(`{
				viewer {
					repositories(first: 100, ownerAffiliations: [OWNER, COLLABORATOR, ORGANIZATION_MEMBER]%s) {
						nodes {
							nameWithOwner
							pullRequests(states: OPEN) { totalCount }
						}
						pageInfo { hasNextPage endCursor }
					}
				}
			}`, after)

			if err := c.do(query, nil, &result); err != nil {
				return nil, fmt.Errorf("discover repos: %w", err)
			}

			for _, node := range result.Viewer.Repositories.Nodes {
				if node.PullRequests.TotalCount > 0 && !seen[node.NameWithOwner] {
					seen[node.NameWithOwner] = true
					repos = append(repos, node.NameWithOwner)
				}
			}

			if !result.Viewer.Repositories.PageInfo.HasNextPage {
				break
			}
			cursor = &result.Viewer.Repositories.PageInfo.EndCursor
		}
	}

	// Search for open PRs in each org — no read:org permission needed
	for _, org := range orgs {
		var searchCursor *string
		for {
			after := ""
			if searchCursor != nil {
				after = fmt.Sprintf(`, after: %q`, *searchCursor)
			}
			query := fmt.Sprintf(`{
				search(query: "is:open is:pr org:%s", type: ISSUE, first: 100%s) {
					nodes {
						... on PullRequest {
							repository { nameWithOwner }
						}
					}
					pageInfo { hasNextPage endCursor }
				}
			}`, org, after)

			var result struct {
				Search struct {
					Nodes []struct {
						Repository *struct {
							NameWithOwner string `json:"nameWithOwner"`
						} `json:"repository"`
					} `json:"nodes"`
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"search"`
			}

			if err := c.do(query, nil, &result); err != nil {
				log.Printf("search org %s: %v", org, err)
				break
			}

			for _, node := range result.Search.Nodes {
				if node.Repository != nil && !seen[node.Repository.NameWithOwner] {
					seen[node.Repository.NameWithOwner] = true
					repos = append(repos, node.Repository.NameWithOwner)
				}
			}

			if !result.Search.PageInfo.HasNextPage {
				break
			}
			searchCursor = &result.Search.PageInfo.EndCursor
		}
	}

	return repos, nil
}
