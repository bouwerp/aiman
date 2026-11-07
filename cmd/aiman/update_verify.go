//go:build acceptance

package main

// verifyChecksum checks data against the sha256 line for name in checksums
// (the release's checksums.txt, `<hex>  <name>` per line).
//
// Stub: accepts everything until verification lands.
func verifyChecksum(data []byte, checksums, name string) error {
	_, _, _ = data, checksums, name
	return nil
}

// downloadRelease downloads assetName's archive from release, verified against
// the release's checksums.txt, and returns the extracted binary's path.
//
// Stub: downloads without verifying, as installRelease does today.
func downloadRelease(release *githubRelease, assetName string) (string, error) {
	u, err := release.assetURL(assetName)
	if err != nil {
		return "", err
	}
	return downloadBinary(u, assetName)
}
