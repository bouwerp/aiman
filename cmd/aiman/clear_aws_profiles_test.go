package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	os.Stdout = prev
	w.Close()
	out := <-done
	r.Close()
	return out
}

func newTestRepo(t *testing.T) *sqlite.Repository {
	t.Helper()
	repo, err := sqlite.NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	t.Cleanup(func() { repo.Close() })
	return repo
}

func TestRunClearAWSProfilesClearsAndReports(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID:        "sess-legacy",
		Status:    domain.SessionStatusActive,
		CreatedAt: time.Now(),
		AWSConfig: &domain.AWSConfig{SourceProfile: "aiman-58f485ff", Region: "us-east-2"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var runErr error
	out := captureStdout(t, func() { runErr = runClearAWSProfiles(repo, nil) })
	if runErr != nil {
		t.Fatalf("runClearAWSProfiles: %v", runErr)
	}
	if !strings.Contains(out, "sess-legacy") || !strings.Contains(out, "aiman-58f485ff") {
		t.Fatalf("expected the cleared reference in the output, got:\n%s", out)
	}

	got, err := repo.Get(ctx, "sess-legacy")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AWSConfig == nil || got.AWSConfig.SourceProfile != "" {
		t.Fatalf("expected the stale profile to be cleared, got %+v", got.AWSConfig)
	}
	if got.AWSConfig.Region != "us-east-2" {
		t.Fatalf("expected the region to survive, got %q", got.AWSConfig.Region)
	}
}

func TestRunClearAWSProfilesOnCleanDatabase(t *testing.T) {
	repo := newTestRepo(t)
	if err := repo.Save(context.Background(), &domain.Session{
		ID:        "sess-clean",
		Status:    domain.SessionStatusActive,
		CreatedAt: time.Now(),
		AWSConfig: &domain.AWSConfig{SourceProfile: "prod", Region: "us-east-2"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var runErr error
	out := captureStdout(t, func() { runErr = runClearAWSProfiles(repo, nil) })
	if runErr != nil {
		t.Fatalf("runClearAWSProfiles: %v", runErr)
	}
	if !strings.Contains(out, "No legacy") {
		t.Fatalf("expected a no-op message, got:\n%s", out)
	}
}

func TestRunClearAWSProfilesRejectsArguments(t *testing.T) {
	repo := newTestRepo(t)
	if err := runClearAWSProfiles(repo, []string{"prod"}); err == nil {
		t.Fatal("expected an error for an unexpected positional argument")
	}
}
