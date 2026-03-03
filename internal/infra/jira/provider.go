package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

type Config struct {
	URL      string
	Email    string
	APIToken string
}

type Provider struct {
	config Config
	client *http.Client
}

func NewProvider(config Config) *Provider {
	return &Provider{
		config: config,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

type jiraIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string `json:"summary"`
		Description string `json:"description"`
		Status      struct {
			Name string `json:"name"`
		} `json:"status"`
		Assignee struct {
			DisplayName string `json:"displayName"`
		} `json:"assignee"`
		Created string `json:"created"`
		Updated string `json:"updated"`
	} `json:"fields"`
}

type jiraSearchResponse struct {
	Issues []jiraIssue `json:"issues"`
}

func (p *Provider) SearchIssues(ctx context.Context, query string) ([]domain.Issue, error) {
	// Simple JQL search based on the query. For now, assuming query is part of the summary.
	jql := fmt.Sprintf("summary ~ %q", query)
	
	u, err := url.Parse(p.config.URL + "/rest/api/3/search/jql")
	if err != nil {
		return nil, fmt.Errorf("failed to parse jira url: %w", err)
	}

	params := url.Values{}
	params.Add("jql", jql)
	u.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(p.config.Email, p.config.APIToken)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to search issues: status %s, body %s", resp.Status, string(body))
	}

	var searchResp jiraSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	issues := make([]domain.Issue, len(searchResp.Issues))
	for i, ji := range searchResp.Issues {
		issues[i] = p.toDomainIssue(ji)
	}

	return issues, nil
}

func (p *Provider) GetIssue(ctx context.Context, key string) (domain.Issue, error) {
	u, err := url.Parse(p.config.URL + "/rest/api/3/issue/" + key)
	if err != nil {
		return domain.Issue{}, fmt.Errorf("failed to parse jira url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return domain.Issue{}, fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(p.config.Email, p.config.APIToken)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return domain.Issue{}, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return domain.Issue{}, fmt.Errorf("failed to get issue: status %s, body %s", resp.Status, string(body))
	}

	var ji jiraIssue
	if err := json.NewDecoder(resp.Body).Decode(&ji); err != nil {
		return domain.Issue{}, fmt.Errorf("failed to decode response: %w", err)
	}

	return p.toDomainIssue(ji), nil
}

func (p *Provider) toDomainIssue(ji jiraIssue) domain.Issue {
	created, _ := time.Parse(time.RFC3339, ji.Fields.Created)
	updated, _ := time.Parse(time.RFC3339, ji.Fields.Updated)

	return domain.Issue{
		ID:          ji.Key,
		Key:         ji.Key,
		Summary:     ji.Fields.Summary,
		Description: ji.Fields.Description,
		Status:      domain.IssueStatus(ji.Fields.Status.Name),
		Assignee:    ji.Fields.Assignee.DisplayName,
		CreatedAt:   created,
		UpdatedAt:   updated,
	}
}
