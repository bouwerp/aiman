package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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
	if query != "" {
		// Search by summary or key across all statuses (so Done issues are findable too)
		jql := fmt.Sprintf("(summary ~ %q OR key = %s) ORDER BY created DESC", query, query)
		return p.fetchIssues(ctx, jql, 100)
	}

	// Default: fetch the user's own open issues and 'Dev Ready' issues,
	// then append recent open issues from others.
	myIssues, err := p.fetchIssues(ctx,
		"(assignee = currentUser() OR status = \"Dev Ready\") AND statusCategory != \"Done\" ORDER BY created DESC", 100)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(myIssues))
	for _, issue := range myIssues {
		seen[issue.Key] = true
	}

	otherIssues, _ := p.fetchIssues(ctx,
		"assignee != currentUser() AND statusCategory != \"Done\" ORDER BY created DESC", 50)
	for _, issue := range otherIssues {
		if !seen[issue.Key] {
			myIssues = append(myIssues, issue)
		}
	}

	return myIssues, nil
}

func (p *Provider) fetchIssues(ctx context.Context, jql string, maxResults int) ([]domain.Issue, error) {
	u, err := url.Parse(p.config.URL + "/rest/api/3/search/jql")
	if err != nil {
		return nil, fmt.Errorf("failed to parse jira url: %w", err)
	}

	params := url.Values{}
	params.Add("jql", jql)
	params.Add("fields", "summary,description,status,assignee,created,updated")
	params.Add("maxResults", fmt.Sprintf("%d", maxResults))
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

type jiraTransition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   struct {
		Name string `json:"name"`
	} `json:"to"`
}

type jiraTransitionsResponse struct {
	Transitions []jiraTransition `json:"transitions"`
}

type jiraTransitionRequest struct {
	Transition struct {
		ID string `json:"id"`
	} `json:"transition"`
}

func (p *Provider) TransitionIssue(ctx context.Context, key string, status string) error {
	if status == "" {
		return nil
	}

	// 1. Fetch available transitions
	u, err := url.Parse(fmt.Sprintf("%s/rest/api/3/issue/%s/transitions", p.config.URL, key))
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(p.config.Email, p.config.APIToken)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to get transitions: %s", string(body))
	}

	var transResp jiraTransitionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&transResp); err != nil {
		return err
	}

	// 2. Find transition that moves to the target status name
	var targetID string
	for _, t := range transResp.Transitions {
		// Match against the destination status name OR the transition name itself
		if strings.EqualFold(t.To.Name, status) || strings.EqualFold(t.Name, status) {
			targetID = t.ID
			break
		}
	}

	if targetID == "" {
		// Not found is not necessarily an error (could already be in that status),
		// but we can log or return it if it's considered a misconfiguration.
		return fmt.Errorf("no transition found for status %q", status)
	}

	// 3. Trigger transition
	var transReq jiraTransitionRequest
	transReq.Transition.ID = targetID
	body, _ := json.Marshal(transReq)

	req, err = http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.SetBasicAuth(p.config.Email, p.config.APIToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err = p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to transition issue: %s", string(body))
	}

	return nil
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

	desc := formatIssueDescription(ji.Fields.Description)

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
