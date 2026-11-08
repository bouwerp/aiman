package remotesvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"
)

// readyAsset is the asset the release workflow publishes last. The pinned
// installer refuses a release without it, so its presence is what makes a
// release installable.
const readyAsset = "checksums.txt"

// Release is what GitHub reports about the latest published release.
type Release struct {
	Tag         string
	Assets      []string
	ETag        string
	PublishedAt time.Time
}

// Ready reports whether the release can be installed: its checksums.txt asset
// exists.
func (r Release) Ready() bool {
	return slices.Contains(r.Assets, readyAsset)
}

// LatestRelease GETs releases/latest with If-None-Match when etag is set.
// A 304 returns (Release{}, false, nil); GitHub does not count it against the
// unauthenticated rate limit. A tag that fails releaseTagRe is an error.
func LatestRelease(ctx context.Context, etag string) (Release, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return Release{}, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "aiman-cli")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Release{}, false, fmt.Errorf("look up latest release: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return Release{}, false, nil
	case http.StatusOK:
	default:
		return Release{}, false, fmt.Errorf("look up latest release: GitHub API returned %d", resp.StatusCode)
	}
	var body struct {
		TagName     string    `json:"tag_name"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Release{}, false, fmt.Errorf("look up latest release: %w", err)
	}
	tag, ok := ReleaseTag(body.TagName)
	if !ok {
		return Release{}, false, fmt.Errorf("look up latest release: unexpected tag %q", body.TagName)
	}
	rel := Release{Tag: tag, ETag: resp.Header.Get("ETag"), PublishedAt: body.PublishedAt}
	for _, a := range body.Assets {
		rel.Assets = append(rel.Assets, a.Name)
	}
	return rel, true, nil
}
