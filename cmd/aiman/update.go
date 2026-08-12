package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const githubReleasesAPI = "https://api.github.com/repos/bouwerp/aiman/releases/latest"

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
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

	assetName := platformAssetName()
	var downloadURL string
	for _, a := range release.Assets {
		if a.Name == assetName+".tar.gz" {
			downloadURL = a.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no pre-built binary found for %s/%s (asset: %s)", runtime.GOOS, runtime.GOARCH, assetName)
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

	fmt.Printf("Updated to %s successfully.\n", release.TagName)
	return nil
}

func fetchLatestRelease() (*githubRelease, error) {
	req, err := http.NewRequest(http.MethodGet, githubReleasesAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}
	return &release, nil
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
