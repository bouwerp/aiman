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
			Timeout: 30 * time.Second,
		},
	}
}

type jiraIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string      `json:"summary"`
		Description interface{} `json:"description"`
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
	var jql string
	if query == "" {
		// Find most recently updated issues that aren't closed
		jql = "statusCategory != \"Done\" ORDER BY updated DESC"
	} else {
		// Search both summary (quoted) and key (unquoted) if query is provided
		jql = fmt.Sprintf("(summary ~ %q OR key = %s) ORDER BY updated DESC", query, query)
	}
	
	u, err := url.Parse(p.config.URL + "/rest/api/3/search/jql")
	if err != nil {
		return nil, fmt.Errorf("failed to parse jira url: %w", err)
	}

	params := url.Values{}
	params.Add("jql", jql)
	params.Add("fields", "summary,description,status,assignee,created,updated")
	params.Add("maxResults", "50")
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
	// JIRA v3 uses a specific ISO8601 format: 2026-03-03T12:45:19.036-0600
	created, _ := time.Parse("2006-01-02T15:04:05.000-0700", ji.Fields.Created)
	updated, _ := time.Parse("2006-01-02T15:04:05.000-0700", ji.Fields.Updated)

	assignee := "Unassigned"
	if ji.Fields.Assignee.DisplayName != "" {
		assignee = ji.Fields.Assignee.DisplayName
	}

	desc := ""
	if ji.Fields.Description != nil {
		desc = "(Rich Text Content)"
	}

	return domain.Issue{
		ID:          ji.Key,
		Key:         ji.Key,
		Summary:     ji.Fields.Summary,
		Description: desc,
		Status:      domain.IssueStatus(ji.Fields.Status.Name),
		Assignee:    assignee,
		CreatedAt:   created,
		UpdatedAt:   updated,
	}
}

