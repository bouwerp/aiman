package images

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// tarEntry is one entry of a test layer.
type tarEntry struct {
	Name     string
	Body     string
	Type     byte
	Linkname string
}

func layerTar(t *testing.T, gz bool, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		typ := e.Type
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.Name, Typeflag: typ, Mode: 0o644, Linkname: e.Linkname, ModTime: time.Unix(0, 0)}
		if typ == tar.TypeDir {
			hdr.Mode = 0o755
		}
		if typ == tar.TypeReg {
			hdr.Size = int64(len(e.Body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.Body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if !gz {
		return buf.Bytes()
	}
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	_, _ = zw.Write(buf.Bytes())
	_ = zw.Close()
	return out.Bytes()
}

// testImage is an image the fake registry serves.
type testImage struct {
	Manifest []byte
	Digest   string
	Config   []byte
	Layers   [][]byte
}

func newTestImage(t *testing.T, layers ...[]byte) testImage {
	t.Helper()
	var diffIDs []string
	for _, l := range layers {
		diffIDs = append(diffIDs, sha256Digest(l))
	}
	cfg, _ := json.Marshal(map[string]any{
		"architecture": HostPlatform().Architecture, "os": "linux",
		"config": map[string]any{"Cmd": []string{"/bin/true"}},
		"rootfs": map[string]any{"type": "layers", "diff_ids": diffIDs},
	})
	m := map[string]any{
		"schemaVersion": 2, "mediaType": MediaOCIManifest,
		"config": Descriptor{MediaType: "application/vnd.oci.image.config.v1+json", Digest: sha256Digest(cfg), Size: int64(len(cfg))},
	}
	var ls []Descriptor
	for _, l := range layers {
		ls = append(ls, Descriptor{MediaType: "application/vnd.oci.image.layer.v1.tar", Digest: sha256Digest(l), Size: int64(len(l))})
	}
	m["layers"] = ls
	mb, _ := json.Marshal(m)
	return testImage{Manifest: mb, Digest: sha256Digest(mb), Config: cfg, Layers: layers}
}

// fakeRegistry serves manifests and blobs behind a bearer challenge.
type fakeRegistry struct {
	t         *testing.T
	srv       *httptest.Server
	mu        sync.Mutex
	manifests map[string][]byte // "repo:ref" -> body
	media     map[string]string
	blobs     map[string][]byte
	// corrupt serves other bytes for this blob digest.
	corrupt   string
	blobGets  int
	signed    map[string]bool
	tokenGets int
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	f := &fakeRegistry{t: t, manifests: map[string][]byte{}, media: map[string]string{}, blobs: map[string][]byte{}, signed: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRegistry) host() string { return strings.TrimPrefix(f.srv.URL, "http://") }

func (f *fakeRegistry) registry() *HTTPRegistry {
	return &HTTPRegistry{PlainHTTP: true, DockerConfig: "/nonexistent"}
}

func (f *fakeRegistry) push(repo, tag string, img testImage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manifests[repo+":"+tag] = img.Manifest
	f.manifests[repo+":"+img.Digest] = img.Manifest
	f.media[img.Digest] = MediaOCIManifest
	f.blobs[sha256Digest(img.Config)] = img.Config
	for _, l := range img.Layers {
		f.blobs[sha256Digest(l)] = l
	}
}

// pushIndex tags an index over imgs, one per platform.
func (f *fakeRegistry) pushIndex(repo, tag string, plats []Platform, imgs []testImage) string {
	var ds []Descriptor
	for i, img := range imgs {
		f.push(repo, img.Digest, img)
		p := plats[i]
		ds = append(ds, Descriptor{MediaType: MediaOCIManifest, Digest: img.Digest, Size: int64(len(img.Manifest)), Platform: &p})
	}
	b, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": MediaOCIIndex, "manifests": ds})
	f.mu.Lock()
	f.manifests[repo+":"+tag] = b
	f.manifests[repo+":"+sha256Digest(b)] = b
	f.media[sha256Digest(b)] = MediaOCIIndex
	f.mu.Unlock()
	return sha256Digest(b)
}

func (f *fakeRegistry) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		f.mu.Lock()
		f.tokenGets++
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "tok"})
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+f.srv.URL+`/token",service="fake"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v2/")
	f.mu.Lock()
	defer f.mu.Unlock()
	if i := strings.Index(path, "/manifests/"); i >= 0 {
		repo, ref := path[:i], path[i+len("/manifests/"):]
		if strings.HasSuffix(ref, ".sig") {
			if f.signed[ref] {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
			return
		}
		b, ok := f.manifests[repo+":"+ref]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", f.media[sha256Digest(b)])
		_, _ = w.Write(b)
		return
	}
	if i := strings.Index(path, "/blobs/"); i >= 0 {
		d := path[i+len("/blobs/"):]
		f.blobGets++
		b, ok := f.blobs[d]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if d == f.corrupt {
			b = bytes.ToUpper(append([]byte(nil), b...))
			b[0] ^= 0xff
		}
		_, _ = w.Write(b)
		return
	}
	http.NotFound(w, r)
}
