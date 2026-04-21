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
		if _, err := io.Copy(tmp, tr); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return "", fmt.Errorf("write temp: %w", err)
		}
		tmp.Close()
		if err := os.Chmod(tmp.Name(), 0755); err != nil {
			os.Remove(tmp.Name())
			return "", err
		}
		return tmp.Name(), nil
	}

	return "", fmt.Errorf("binary %q not found in archive", binaryName)
}

// replaceExecutable atomically replaces the running binary.
// On POSIX systems: write new binary to a temp file beside the target, then rename.
func replaceExecutable(target, newBinary string) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".aiman-update-*")
	if err != nil {
		return fmt.Errorf("create staging file: %w", err)
	}
	tmpName := tmp.Name()
	tmp.Close()

	src, err := os.Open(newBinary)
	if err != nil {
		os.Remove(tmpName)
		return err
	}
	dst, err := os.OpenFile(tmpName, os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		src.Close()
		os.Remove(tmpName)
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		src.Close()
		dst.Close()
		os.Remove(tmpName)
		return fmt.Errorf("copy to staging: %w", err)
	}
	src.Close()
	dst.Close()

	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename staging to target: %w", err)
	}
	return nil
}
