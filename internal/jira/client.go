package jira

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type Issue struct {
	Key         string
	Summary     string
	Status      string
	EpicKey     string
	EpicSummary string
}

type Client struct {
	baseURL    string
	authHeader string
	httpClient *http.Client
}

func NewClient(baseURL, email, token string) *Client {
	cred := base64.StdEncoding.EncodeToString([]byte(email + ":" + token))
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		authHeader: "Basic " + cred,
		httpClient: &http.Client{},
	}
}

func (c *Client) FetchIssues(keys []string) (map[string]*Issue, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	// Build JQL: key in (SLIDE-1234, SLIDE-5678)
	quoted := make([]string, len(keys))
	for i, k := range keys {
		quoted[i] = k
	}
	jql := "key in (" + strings.Join(quoted, ",") + ")"

	u := c.baseURL + "/rest/api/3/search?jql=" + url.QueryEscape(jql) +
		"&fields=summary,status,parent&maxResults=" + fmt.Sprintf("%d", len(keys))

	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("jira request: %w", err)
	}
	req.Header.Set("Authorization", c.authHeader)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jira fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("jira API %d", resp.StatusCode)
	}

	var result struct {
		Issues []struct {
			Key    string `json:"key"`
			Fields struct {
				Summary string `json:"summary"`
				Status  struct {
					Name string `json:"name"`
				} `json:"status"`
				Parent *struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"parent"`
			} `json:"fields"`
		} `json:"issues"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("jira decode: %w", err)
	}

	issues := make(map[string]*Issue, len(result.Issues))
	for _, i := range result.Issues {
		issue := &Issue{
			Key:     i.Key,
			Summary: i.Fields.Summary,
			Status:  i.Fields.Status.Name,
		}
		if i.Fields.Parent != nil {
			issue.EpicKey = i.Fields.Parent.Key
			issue.EpicSummary = i.Fields.Parent.Fields.Summary
		}
		issues[i.Key] = issue
	}
	return issues, nil
}
