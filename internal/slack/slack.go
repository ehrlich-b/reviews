package slack

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type SlackUser struct {
	ID          string
	DisplayName string
	RealName    string
}

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

func (c *Client) SendDM(userID, text string) error {
	body := fmt.Sprintf(`{"channel":%q,"text":%q}`, userID, text)
	req, err := http.NewRequest("POST", "https://slack.com/api/chat.postMessage", strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("slack send: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("slack decode: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("slack error: %s", result.Error)
	}
	return nil
}

func (c *Client) ListUsers() ([]SlackUser, error) {
	req, err := http.NewRequest("GET", "https://slack.com/api/users.list", nil)
	if err != nil {
		return nil, fmt.Errorf("slack request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack list users: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		OK      bool `json:"ok"`
		Members []struct {
			ID      string `json:"id"`
			Profile struct {
				DisplayName string `json:"display_name"`
				RealName    string `json:"real_name"`
			} `json:"profile"`
			Deleted bool `json:"deleted"`
			IsBot   bool `json:"is_bot"`
		} `json:"members"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("slack decode: %w", err)
	}
	if !result.OK {
		return nil, fmt.Errorf("slack error: %s", result.Error)
	}

	var users []SlackUser
	for _, m := range result.Members {
		if m.Deleted || m.IsBot {
			continue
		}
		users = append(users, SlackUser{
			ID:          m.ID,
			DisplayName: m.Profile.DisplayName,
			RealName:    m.Profile.RealName,
		})
	}
	return users, nil
}
