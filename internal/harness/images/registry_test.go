package images

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseReference(t *testing.T) {
	cases := map[string]string{
		"postgres:16":                        "registry-1.docker.io/library/postgres:16",
		"redis":                              "registry-1.docker.io/library/redis:latest",
		"docker.io/bitnami/redis:7":          "registry-1.docker.io/bitnami/redis:7",
		"ghcr.io/o/r/app:v1":                 "ghcr.io/o/r/app:v1",
		"localhost:5000/x@sha256:" + hex64(): "localhost:5000/x@sha256:" + hex64(),
	}
	for in, want := range cases {
		r, err := ParseReference(in)
		if err != nil || r.String() != want {
			t.Errorf("ParseReference(%q) = %q, %v; want %q", in, r.String(), err, want)
		}
	}
	for _, bad := range []string{"UPPER/x", "x@sha256:12", "a/../b", "x:bad tag"} {
		if _, err := ParseReference(bad); err == nil {
			t.Errorf("ParseReference(%q) accepted", bad)
		}
	}
}

func hex64() string { return "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" }

func TestResolvesToPlatformDigest(t *testing.T) {
	reg := newFakeRegistry(t)
	host := newTestImage(t, layerTar(t, true, tarEntry{Name: "host.txt", Body: "host"}))
	other := newTestImage(t, layerTar(t, true, tarEntry{Name: "other.txt", Body: "other"}))
	otherPlat := Platform{OS: "linux", Architecture: "s390x"}
	idx := reg.pushIndex("o/redis", "7", []Platform{otherPlat, HostPlatform()}, []testImage{other, host})

	r := reg.registry()
	res, err := r.Resolve(context.Background(), reg.host()+"/o/redis:7", HostPlatform())
	if err != nil {
		t.Fatal(err)
	}
	if res.Digest != host.Digest || res.IndexDigest != idx {
		t.Fatalf("resolved %s (index %s), want the host platform's %s (index %s)", res.Digest, res.IndexDigest, host.Digest, idx)
	}
	if res.Config.Digest != sha256Digest(host.Config) || len(res.Layers) != 1 || res.Platform != HostPlatform().String() {
		t.Fatalf("resolved manifest = %+v", res)
	}
	if reg.blobGets != 0 {
		t.Fatalf("resolve fetched %d blobs; it reads manifests only", reg.blobGets)
	}
	// A ref pinned to the index digest resolves the same way.
	pinned, err := r.Resolve(context.Background(), reg.host()+"/o/redis@"+idx, HostPlatform())
	if err != nil || pinned.Digest != host.Digest {
		t.Fatalf("pinned resolve = %s, %v", pinned.Digest, err)
	}
	if _, err := r.Resolve(context.Background(), reg.host()+"/o/redis:7", Platform{OS: "linux", Architecture: "mips"}); err == nil {
		t.Fatal("a platform the index lacks resolved")
	}
}

func TestFetchVerifiesBlobs(t *testing.T) {
	reg := newFakeRegistry(t)
	img := newTestImage(t, layerTar(t, true, tarEntry{Name: "a", Body: "a"}), layerTar(t, false, tarEntry{Name: "b", Body: "b"}))
	reg.push("o/app", "1", img)
	r := reg.registry()
	res, err := r.Resolve(context.Background(), reg.host()+"/o/app:1", HostPlatform())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "q")
	if err := Fetch(context.Background(), r, res, dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := Verify(dir, res.Digest)
	if err != nil || cfg != sha256Digest(img.Config) {
		t.Fatalf("Verify = %s, %v", cfg, err)
	}
	// A blob changed on disk after the fetch fails the check.
	if err := os.WriteFile(blobPath(dir, sha256Digest(img.Layers[1])), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(dir, res.Digest); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Verify of a changed blob = %v", err)
	}

	reg.corrupt = sha256Digest(img.Layers[0])
	dir2 := filepath.Join(t.TempDir(), "q2")
	if err := Fetch(context.Background(), r, res, dir2); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Fetch of a corrupt blob = %v", err)
	}
	if _, err := os.Stat(dir2); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a failed fetch left the quarantine behind")
	}
}

func TestPinnedDigestMismatchFails(t *testing.T) {
	reg := newFakeRegistry(t)
	host := newTestImage(t, layerTar(t, true, tarEntry{Name: "host.txt", Body: "host"}))
	idx := reg.pushIndex("o/busybox", "1.36", []Platform{HostPlatform()}, []testImage{host})
	other := newTestImage(t, layerTar(t, true, tarEntry{Name: "other.txt", Body: "other"}))
	otherIdx := reg.pushIndex("o/busybox", "evil", []Platform{HostPlatform()}, []testImage{other})
	// The registry answers the pinned index digest with other bytes.
	reg.mu.Lock()
	reg.manifests["o/busybox:"+idx] = reg.manifests["o/busybox:"+otherIdx]
	reg.mu.Unlock()
	if _, err := reg.registry().Resolve(context.Background(), reg.host()+"/o/busybox:1.36@"+idx, HostPlatform()); err == nil || !strings.Contains(err.Error(), "other bytes") {
		t.Fatalf("resolve = %v, want a digest mismatch", err)
	}
}
