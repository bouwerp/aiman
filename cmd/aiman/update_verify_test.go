package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestVerifyChecksum(t *testing.T) {
	data := []byte("the real binary")
	other := []byte("a tampered binary")
	sums := fmt.Sprintf("%s  aiman-linux-arm64.tar.gz\n%s  aiman-linux-amd64.tar.gz\n", sha256Hex(other), sha256Hex(data))

	if err := verifyChecksum(data, sums, "aiman-linux-amd64.tar.gz"); err != nil {
		t.Fatalf("matching checksum refused: %v", err)
	}
	if err := verifyChecksum(other, sums, "aiman-linux-amd64.tar.gz"); err == nil {
		t.Fatal("mismatched checksum accepted")
	}
	if err := verifyChecksum(data, sums, "aiman-darwin-arm64.tar.gz"); err == nil {
		t.Fatal("asset missing from checksums.txt accepted")
	}
	if err := verifyChecksum(data, "", "aiman-linux-amd64.tar.gz"); err == nil {
		t.Fatal("empty checksums.txt accepted")
	}
	if err := verifyChecksum(data, sha256Hex(data)+"  aiman-linux-amd64.tar.gz.bak\n", "aiman-linux-amd64.tar.gz"); err == nil {
		t.Fatal("prefix match on asset name accepted")
	}
}

// tarGz packs one file named name holding body.
func tarGz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeRelease serves archive and checksums (when non-empty) as release assets.
func fakeRelease(t *testing.T, assetName string, archive []byte, checksums string) *githubRelease {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/dl/"+assetName+".tar.gz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		if checksums == "" {
			http.NotFound(w, nil)
			return
		}
		_, _ = w.Write([]byte(checksums))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &githubRelease{TagName: "v9.9.9", Assets: []githubAsset{
		{Name: assetName + ".tar.gz", BrowserDownloadURL: srv.URL + "/dl/" + assetName + ".tar.gz"},
		{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/dl/checksums.txt"},
	}}
}

func TestDownloadReleaseVerifiesChecksum(t *testing.T) {
	asset := platformAssetName()
	good := tarGz(t, asset, []byte("good binary"))
	bad := tarGz(t, asset, []byte("tampered binary"))
	sums := fmt.Sprintf("%s  %s.tar.gz\n", sha256Hex(good), asset)

	path, err := downloadRelease(fakeRelease(t, asset, good, sums), asset)
	if err != nil {
		t.Fatalf("good archive refused: %v", err)
	}
	got, _ := os.ReadFile(path) // #nosec G304 -- temp file the function returned
	_ = os.Remove(path)
	if string(got) != "good binary" {
		t.Fatalf("extracted %q", got)
	}

	if path, err := downloadRelease(fakeRelease(t, asset, bad, sums), asset); err == nil {
		_ = os.Remove(path)
		t.Fatal("tampered archive installed: checksum not verified")
	}
	if path, err := downloadRelease(fakeRelease(t, asset, good, ""), asset); err == nil {
		_ = os.Remove(path)
		t.Fatal("release without checksums.txt installed")
	}
}
