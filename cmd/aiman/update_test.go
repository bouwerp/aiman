package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPreviousReleasePicksOlderStable(t *testing.T) {
	rels := []githubRelease{
		{TagName: "v0.9.5"},
		{TagName: "v0.9.4"},
		{TagName: "v0.9.3", Prerelease: true},
		{TagName: "v0.9.2", Draft: true},
		{TagName: "v0.9.1"},
	}
	got, err := previousRelease(rels, "v0.9.5")
	if err != nil {
		t.Fatal(err)
	}
	if got.TagName != "v0.9.4" {
		t.Fatalf("got %s, want v0.9.4", got.TagName)
	}

	got, err = previousRelease(rels, "0.9.4")
	if err != nil {
		t.Fatal(err)
	}
	if got.TagName != "v0.9.1" {
		t.Fatalf("got %s, want v0.9.1 (skip prerelease and draft)", got.TagName)
	}
}

func TestPreviousReleaseWhenCurrentUnknownUsesSecondStable(t *testing.T) {
	rels := []githubRelease{
		{TagName: "v0.9.5"},
		{TagName: "v0.9.4"},
	}
	got, err := previousRelease(rels, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if got.TagName != "v0.9.4" {
		t.Fatalf("got %s, want v0.9.4", got.TagName)
	}
}

func TestPreviousReleaseOldestErrors(t *testing.T) {
	rels := []githubRelease{{TagName: "v0.9.1"}}
	_, err := previousRelease(rels, "v0.9.1")
	if err == nil {
		t.Fatal("expected error when there is no older release")
	}
	if !errors.Is(err, errNoOlderRelease) {
		t.Fatalf("err = %v, want errNoOlderRelease", err)
	}
}

func TestPreviousReleaseEmptyListErrors(t *testing.T) {
	_, err := previousRelease(nil, "dev")
	if !errors.Is(err, errNoOlderRelease) {
		t.Fatalf("err = %v, want errNoOlderRelease", err)
	}
}

func TestNormalizeReleaseTag(t *testing.T) {
	if got := normalizeReleaseTag("v0.9.1"); got != "v0.9.1" {
		t.Fatalf("got %q", got)
	}
	if got := normalizeReleaseTag("0.9.1"); got != "v0.9.1" {
		t.Fatalf("got %q", got)
	}
	if got := normalizeReleaseTag("  V0.9.1  "); got != "v0.9.1" {
		t.Fatalf("got %q", got)
	}
}

func TestReleaseAssetURL(t *testing.T) {
	r := githubRelease{
		TagName: "v0.9.1",
		Assets: []githubAsset{
			{Name: "aiman-linux-amd64.tar.gz", BrowserDownloadURL: "https://example/linux"},
			{Name: "notes.md", BrowserDownloadURL: "https://example/notes"},
		},
	}
	got, err := r.assetURL("aiman-linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example/linux" {
		t.Fatalf("got %q", got)
	}
	if _, err := r.assetURL("aiman-darwin-arm64"); err == nil {
		t.Fatal("expected missing-asset error")
	}
}

func TestFetchReleaseByTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/tags/v0.9.1" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(githubRelease{
			TagName: "v0.9.1",
			Assets: []githubAsset{
				{Name: "aiman-linux-amd64.tar.gz", BrowserDownloadURL: "https://example/linux"},
			},
		})
	}))
	defer srv.Close()
	swapGitHubAPI(t, srv.URL)

	got, err := fetchReleaseByTag("v0.9.1")
	if err != nil {
		t.Fatal(err)
	}
	if got.TagName != "v0.9.1" {
		t.Fatalf("tag = %s", got.TagName)
	}
	if len(got.Assets) != 1 {
		t.Fatalf("assets = %d", len(got.Assets))
	}
}

func TestFetchReleaseByTagNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	swapGitHubAPI(t, srv.URL)

	_, err := fetchReleaseByTag("v9.9.9")
	if !errors.Is(err, errReleaseNotFound) {
		t.Fatalf("err = %v, want errReleaseNotFound", err)
	}
}

func TestFetchReleases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `[{"tag_name":"v0.9.5"},{"tag_name":"v0.9.4","prerelease":true}]`)
	}))
	defer srv.Close()
	swapGitHubAPI(t, srv.URL)

	got, err := fetchReleases()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].TagName != "v0.9.5" || !got[1].Prerelease {
		t.Fatalf("got %+v", got)
	}
}

func TestParseDowngradeTag(t *testing.T) {
	got, err := parseDowngradeTag(nil)
	if err != nil || got != "" {
		t.Fatalf("got %q err %v", got, err)
	}
	got, err = parseDowngradeTag([]string{"0.9.1"})
	if err != nil || got != "v0.9.1" {
		t.Fatalf("got %q err %v", got, err)
	}
	if _, err := parseDowngradeTag([]string{"v0.9.1", "extra"}); err == nil {
		t.Fatal("expected error for extra args")
	}
	if _, err := parseDowngradeTag([]string{"   "}); err == nil {
		t.Fatal("expected error for blank tag")
	}
}

func swapGitHubAPI(t *testing.T, url string) {
	t.Helper()
	orig := githubRepoAPI
	githubRepoAPI = url
	t.Cleanup(func() { githubRepoAPI = orig })
}
