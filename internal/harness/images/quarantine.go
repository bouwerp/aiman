package images

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrDigestMismatch is a blob or manifest whose bytes do not match its digest.
var ErrDigestMismatch = errors.New("digest mismatch")

// LocalName is the name a vetted image carries in the engine: the load names
// it so docker image inspect finds it and image prune keeps it.
func LocalName(digest string) string {
	h := strings.TrimPrefix(digest, "sha256:")
	if len(h) > 16 {
		h = h[:16]
	}
	return "aiman.local/harness-vetted:" + h
}

func blobPath(dir, digest string) string {
	return filepath.Join(dir, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))
}

type layoutIndex struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Manifests     []Descriptor `json:"manifests"`
}

// Fetch writes res into an OCI layout at dir: the manifest, the config and
// every layer, each checked against its digest and declared size while it is
// written. A mismatch returns ErrDigestMismatch and removes dir. The layout's
// index names the image LocalName for the load.
func Fetch(ctx context.Context, reg Registry, res Resolved, dir string) error {
	part := dir + ".part"
	_ = os.RemoveAll(part)
	if err := fetchInto(ctx, reg, res, part); err != nil {
		_ = os.RemoveAll(part)
		_ = os.RemoveAll(dir)
		return err
	}
	_ = os.RemoveAll(dir)
	return os.Rename(part, dir)
}

func fetchInto(ctx context.Context, reg Registry, res Resolved, dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0o700); err != nil {
		return err
	}
	if sha256Digest(res.Manifest) != res.Digest {
		return fmt.Errorf("manifest %s: %w", res.Digest, ErrDigestMismatch)
	}
	if err := os.WriteFile(blobPath(dir, res.Digest), res.Manifest, 0o600); err != nil {
		return err
	}
	for _, d := range append([]Descriptor{res.Config}, res.Layers...) {
		if err := fetchBlob(ctx, reg, res.Ref, d, blobPath(dir, d.Digest)); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o600); err != nil {
		return err
	}
	idx := layoutIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, Manifests: []Descriptor{{
		MediaType: res.MediaType, Digest: res.Digest, Size: int64(len(res.Manifest)),
		Annotations: map[string]string{
			"io.containerd.image.name":          "docker.io/" + LocalName(res.Digest),
			"org.opencontainers.image.ref.name": strings.SplitN(LocalName(res.Digest), ":", 2)[1],
		},
	}}}
	b, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "index.json"), b, 0o600)
}

func fetchBlob(ctx context.Context, reg Registry, ref Reference, d Descriptor, path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil // an image may list one layer twice
	}
	body, err := reg.Blob(ctx, ref, d.Digest)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // G304: a path under the quarantine named by a checked digest
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, d.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("fetch %s: %w", d.Digest, err)
	}
	if n != d.Size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != d.Digest {
		return fmt.Errorf("blob %s (%d of %d bytes): %w", d.Digest, n, d.Size, ErrDigestMismatch)
	}
	return nil
}

// Verify re-reads the layout at dir and checks that its index names digest
// and that the manifest, the config and every layer hash to their digests.
// It returns the config digest.
func Verify(dir, digest string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "index.json")) //nolint:gosec // G304: the quarantine's own index
	if err != nil {
		return "", err
	}
	var idx layoutIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return "", err
	}
	if len(idx.Manifests) != 1 || idx.Manifests[0].Digest != digest {
		return "", fmt.Errorf("quarantine index does not name %s: %w", digest, ErrDigestMismatch)
	}
	manifest, err := readChecked(dir, Descriptor{Digest: digest, Size: -1})
	if err != nil {
		return "", err
	}
	doc, err := parseManifest(manifest, idx.Manifests[0].MediaType)
	if err != nil {
		return "", err
	}
	for _, d := range append([]Descriptor{doc.Config}, doc.Layers...) {
		if err := hashFile(blobPath(dir, d.Digest), d); err != nil {
			return "", err
		}
	}
	return doc.Config.Digest, nil
}

func readChecked(dir string, d Descriptor) ([]byte, error) {
	b, err := os.ReadFile(blobPath(dir, d.Digest)) //nolint:gosec // G304: a quarantine blob named by a checked digest
	if err != nil {
		return nil, err
	}
	if sha256Digest(b) != d.Digest {
		return nil, fmt.Errorf("blob %s: %w", d.Digest, ErrDigestMismatch)
	}
	return b, nil
}

func hashFile(path string, d Descriptor) error {
	f, err := os.Open(path) //nolint:gosec // G304: a quarantine blob named by a checked digest
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if (d.Size >= 0 && n != d.Size) || "sha256:"+hex.EncodeToString(h.Sum(nil)) != d.Digest {
		return fmt.Errorf("blob %s: %w", d.Digest, ErrDigestMismatch)
	}
	return nil
}
