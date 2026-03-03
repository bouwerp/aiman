package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestProvider_GetIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"key": "AIMAN-1",
			"fields": {
				"summary": "Fix all bugs",
				"description": "Fix them all now",
				"status": { "name": "TODO" },
				"assignee": { "displayName": "Pieter" },
				"created": "2024-03-01T10:00:00Z",
				"updated": "2024-03-01T12:00:00Z"
			}
		}`)
	}))
	defer server.Close()

	config := Config{
		URL:      server.URL,
		Email:    "test@example.com",
		APIToken: "token",
	}

	provider := NewProvider(config)
	issue, err := provider.GetIssue(context.Background(), "AIMAN-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if issue.Key != "AIMAN-1" {
		t.Errorf("expected key AIMAN-1, got %s", issue.Key)
	}
	if issue.Summary != "Fix all bugs" {
		t.Errorf("expected summary 'Fix all bugs', got %s", issue.Summary)
	}
	if issue.Status != domain.IssueStatusTodo {
		t.Errorf("expected status TODO, got %s", issue.Status)
	}
}
