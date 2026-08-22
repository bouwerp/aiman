package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var githubRepoAPI = "https://api.github.com/repos/bouwerp/aiman"

var githubHTTPClient = &http.Client{Timeout: 30 * time.Second}

var (
	errNoOlderRelease  = errors.New("no older stable release")
	errReleaseNotFound = errors.New("release not found")
)

type githubRelease struct {
	TagName    string        `json:"tag_name"`
	Draft      bool          `json:"draft"`
	Prerelease bool          `json:"prerelease"`
	Assets     []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func runUpdate(currentVersion string) error {
	fmt.Println("Checking for updates...")

	release, err := fetchLatestRelease()
	if err != nil {
		return fmt.Errorf("failed to check for updates: %w", err)
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	current := strings.TrimPrefix(currentVersion, "v")

	fmt.Printf("Current version : %s\n", currentVersion)
	fmt.Printf("Latest version  : %s\n", release.TagName)

	if current != "dev" && current == latest {
		fmt.Println("Already up to date.")
		return nil
	}

	if err := installRelease(release); err != nil {
		return err
	}
	fmt.Printf("Updated to %s successfully.\n", release.TagName)
	return nil
}

func runDowngrade(currentVersion string, args []string) error {
	tag, err := parseDowngradeTag(args)
	if err != nil {
		if errors.Is(err, errUsage) {
			return nil
		}
		return err
	}

	var release *githubRelease
	if tag != "" {
		fmt.Printf("Fetching release %s...\n", tag)
		release, err = fetchReleaseByTag(tag)
		if err != nil {
			return fmt.Errorf("failed to fetch release %s: %w", tag, err)
		}
	} else {
		fmt.Println("Looking up the previous stable release...")
		rels, listErr := fetchReleases()
		if listErr != nil {
			return fmt.Errorf("failed to list releases: %w", listErr)
		}
		release, err = previousRelease(rels, currentVersion)
		if err != nil {
			if errors.Is(err, errNoOlderRelease) {
				return fmt.Errorf("%w than %s", err, currentVersion)
			}
			return err
		}
	}

	fmt.Printf("Current version : %s\n", currentVersion)
	fmt.Printf("Target version  : %s\n", release.TagName)

	if err := installRelease(release); err != nil {
		return err
	}
	fmt.Printf("Downgraded to %s successfully.\n", release.TagName)
	return nil
}

func parseDowngradeTag(args []string) (string, error) {
	fs := flag.NewFlagSet("downgrade", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: aiman downgrade [tag]\n\n")
		fmt.Fprintf(os.Stderr, "Replace this binary with an older GitHub release.\n")
		fmt.Fprintf(os.Stderr, "With no tag, installs the previous stable release.\n\n")
		fmt.Fprintf(os.Stderr, "If this binary will not start, reinstall a tag with:\n")
		fmt.Fprintf(os.Stderr, "  curl -sSL https://raw.githubusercontent.com/bouwerp/aiman/main/install.sh | bash -s -- --version v0.9.1\n")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", errUsage
		}
		return "", err
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return "", fmt.Errorf("unexpected argument %q", fs.Arg(1))
	}
	if fs.NArg() == 0 {
		return "", nil
	}
	tag := normalizeReleaseTag(fs.Arg(0))
	if tag == "" {
		return "", fmt.Errorf("invalid release tag %q", fs.Arg(0))
	}
	return tag, nil
}

func installRelease(release *githubRelease) error {
	assetName := platformAssetName()
	downloadURL, err := release.assetURL(assetName)
	if err != nil {
		return err
	}

	fmt.Printf("Downloading %s...\n", assetName)
	newBinary, err := downloadBinary(downloadURL, assetName)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer os.Remove(newBinary)

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine current executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("cannot resolve executable symlink: %w", err)
	}

	if err := replaceExecutable(execPath, newBinary); err != nil {
		return fmt.Errorf("failed to replace binary: %w", err)
	}
	return nil
}

func (r githubRelease) assetURL(assetName string) (string, error) {
	want := assetName + ".tar.gz"
	for _, a := range r.Assets {
		if a.Name == want {
			return a.BrowserDownloadURL, nil
		}
	}
	return "", fmt.Errorf("no pre-built binary found for %s/%s (asset: %s)", runtime.GOOS, runtime.GOARCH, assetName)
}

func normalizeReleaseTag(tag string) string {
	tag = strings.TrimSpace(strings.ToLower(tag))
	tag = strings.TrimPrefix(tag, "v")
	if tag == "" {
		return ""
	}
	return "v" + tag
}

// previousRelease returns the next older stable (non-draft, non-prerelease)
// GitHub release after current. rels must be newest-first, matching the GitHub
// list API. When current is unknown (dev, empty, or not in the list), it
// returns the second stable release so a broken "latest" can still roll back.
func previousRelease(rels []githubRelease, current string) (*githubRelease, error) {
	currentTag := normalizeReleaseTag(current)
	foundCurrent := false
	stables := make([]githubRelease, 0, len(rels))
	for i := range rels {
		r := rels[i]
		if r.Draft || r.Prerelease {
			continue
		}
		if currentTag != "" && normalizeReleaseTag(r.TagName) == currentTag {
			foundCurrent = true
			continue
		}
		if foundCurrent {
			return &rels[i], nil
		}
		stables = append(stables, r)
	}
	if foundCurrent || len(stables) < 2 {
		return nil, errNoOlderRelease
	}
	return &stables[1], nil
}

func fetchLatestRelease() (*githubRelease, error) {
	var release githubRelease
	if err := githubGet(githubRepoAPI+"/releases/latest", &release); err != nil {
		return nil, err
	}
	return &release, nil
}

func fetchReleaseByTag(tag string) (*githubRelease, error) {
	tag = normalizeReleaseTag(tag)
	var release githubRelease
	path := githubRepoAPI + "/releases/tags/" + url.PathEscape(tag)
	if err := githubGet(path, &release); err != nil {
		return nil, err
	}
	return &release, nil
}

func fetchReleases() ([]githubRelease, error) {
	u, err := url.Parse(githubRepoAPI + "/releases")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("per_page", "100")
	u.RawQuery = q.Encode()

	var rels []githubRelease
	if err := githubGet(u.String(), &rels); err != nil {
		return nil, err
	}
	return rels, nil
}

func githubGet(rawURL string, dest any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "aiman-cli")

	resp, err := githubHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return errReleaseNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return err
	}
	return nil
}

func platformAssetName() string {
	os := runtime.GOOS
	arch := runtime.GOARCH
	return fmt.Sprintf("aiman-%s-%s", os, arch)
}

// downloadBinary downloads a .tar.gz asset and extracts the named binary to a temp file.
func downloadBinary(url, assetName string) (string, error) {
	// #nosec G107 -- url is the browser_download_url taken from the GitHub release API
	// response for this repository, not caller-supplied input.
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned %d", resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("gzip open: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	binaryName := assetName // binary inside archive has the same name as the asset
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("tar read: %w", err)
		}
		if filepath.Base(hdr.Name) != binaryName {
			continue
		}

		tmp, err := os.CreateTemp("", "aiman-update-*")
		if err != nil {
			return "", err
		}
		// Cap the extraction: a corrupt or hostile archive must not be able to fill
		// the disk. The real binary is ~16 MB, so 512 MB is generous headroom.
		const maxBinarySize = 512 << 20
		written, err := io.Copy(tmp, io.LimitReader(tr, maxBinarySize+1))
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			return "", fmt.Errorf("write temp: %w", err)
		}
		if written > maxBinarySize {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			return "", fmt.Errorf("release asset %s exceeds the %d byte limit", assetName, int64(maxBinarySize))
		}
		_ = tmp.Close()
		// #nosec G302 -- the extracted file is the aiman binary; it has to carry the
		// executable bit to be runnable after the swap.
		if err := os.Chmod(tmp.Name(), 0755); err != nil {
			_ = os.Remove(tmp.Name())
			return "", err
		}
		return tmp.Name(), nil
	}

	return "", fmt.Errorf("binary %q not found in archive", binaryName)
}

// replaceExecutable atomically replaces the running binary.
// Reads the new binary into memory, writes it to a staging file beside the
// target with explicit 0755 permissions, then renames atomically.
func replaceExecutable(target, newBinary string) error {
	data, err := os.ReadFile(newBinary)
	if err != nil {
		return fmt.Errorf("read new binary: %w", err)
	}

	dir := filepath.Dir(target)
	staged := filepath.Join(dir, ".aiman-update-staged")
	// Remove any stale staging file so WriteFile creates fresh (with correct perms).
	_ = os.Remove(staged)

	// #nosec G306 -- this is the aiman binary being staged for the swap below; it has to
	// be executable, and 0755 matches how the installed binary is expected to sit on disk.
	if err := os.WriteFile(staged, data, 0755); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("write staged binary: %w", err)
	}

	// Ensure executable bit is set regardless of umask.
	// #nosec G302 -- same reason as the staging write above: this is an executable.
	if err := os.Chmod(staged, 0755); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("chmod staged binary: %w", err)
	}

	if err := os.Rename(staged, target); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("replace binary: %w", err)
	}
	return nil
}
