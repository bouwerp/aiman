package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	checksumsAsset = "checksums.txt"

	maxChecksumsSize = 1 << 20
	// The real archive is under 20 MB.
	maxArchiveSize = 512 << 20

	assetDownloadTimeout = 10 * time.Minute
)

// verifyChecksum checks data against the sha256 line for name in checksums
// (the release's checksums.txt, `<hex>  <name>` per line). The name must match
// exactly, and a name with no line is refused.
func verifyChecksum(data []byte, checksums, name string) error {
	want := ""
	for _, line := range strings.Split(checksums, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == name {
			want = f[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("%s is not listed in %s", name, checksumsAsset)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", name, want, got)
	}
	return nil
}

// downloadRelease downloads assetName's archive from release, verified against
// the release's checksums.txt, and returns the extracted binary's path. A
// release without checksums.txt is refused, so nothing published before the
// file existed can be installed.
func downloadRelease(release *githubRelease, assetName string) (string, error) {
	archiveURL, err := release.assetURL(assetName)
	if err != nil {
		return "", err
	}
	noChecksums := fmt.Errorf("release %s has no usable %s; a release without one cannot be installed", release.TagName, checksumsAsset)
	checksumsURL := ""
	for _, a := range release.Assets {
		if a.Name == checksumsAsset {
			checksumsURL = a.BrowserDownloadURL
		}
	}
	if checksumsURL == "" {
		return "", noChecksums
	}
	checksums, err := fetchAsset(checksumsURL, maxChecksumsSize)
	if err != nil {
		return "", fmt.Errorf("%w: %w", noChecksums, err)
	}
	archive, err := fetchAsset(archiveURL, maxArchiveSize)
	if err != nil {
		return "", err
	}
	archiveName := assetName + ".tar.gz"
	if err := verifyChecksum(archive, string(checksums), archiveName); err != nil {
		return "", err
	}
	return extractBinary(bytes.NewReader(archive), assetName)
}

// fetchAsset reads a release asset into memory, because the bytes must be
// verified before anything acts on them.
func fetchAsset(url string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), assetDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// #nosec G107 G704 -- url is the browser_download_url taken from the GitHub release API
	// response for this repository, not caller-supplied input.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("release asset exceeds the %d byte limit", limit)
	}
	return data, nil
}
