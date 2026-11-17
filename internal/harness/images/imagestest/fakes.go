// Package imagestest holds fakes of the image vetter's registry, scanner,
// loader and notifier for tests.
package imagestest

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/bouwerp/aiman/internal/harness/images"
)

// Layer is an uncompressed tar of files.
func Layer(files map[string]string) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	return buf.Bytes()
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// Image is one image the fake registry serves.
type Image struct {
	Manifest []byte
	Config   []byte
	Layers   [][]byte
}

// Digest is the manifest digest.
func (i Image) Digest() string { return digest(i.Manifest) }

// ConfigDigest is the config digest.
func (i Image) ConfigDigest() string { return digest(i.Config) }

// NewImage builds an OCI image of layers; salt makes otherwise equal images
// differ.
func NewImage(salt string, layers ...[]byte) Image {
	return NewImageWith(salt, nil, layers...)
}

// NewImageWith is NewImage with extra fields in the image config's "config"
// object (Entrypoint, Cmd, Env, User, ...); the key "history" goes to the
// config's top-level history list instead.
func NewImageWith(salt string, extra map[string]any, layers ...[]byte) Image {
	inner := map[string]any{"Labels": map[string]string{"salt": salt}}
	var history any
	for k, v := range extra {
		if k == "history" {
			history = v
			continue
		}
		inner[k] = v
	}
	var diffIDs []string
	for _, l := range layers {
		diffIDs = append(diffIDs, digest(l))
	}
	doc := map[string]any{
		"architecture": images.HostPlatform().Architecture, "os": "linux",
		"config": inner,
		"rootfs": map[string]any{"type": "layers", "diff_ids": diffIDs},
	}
	if history != nil {
		doc["history"] = history
	}
	cfg, _ := json.Marshal(doc)
	var ls []images.Descriptor
	for _, l := range layers {
		ls = append(ls, images.Descriptor{MediaType: "application/vnd.oci.image.layer.v1.tar", Digest: digest(l), Size: int64(len(l))})
	}
	m, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": images.MediaOCIManifest,
		"config": images.Descriptor{MediaType: "application/vnd.oci.image.config.v1+json", Digest: digest(cfg), Size: int64(len(cfg))},
		"layers": ls,
	})
	return Image{Manifest: m, Config: cfg, Layers: layers}
}

// Registry is an in-memory registry.
type Registry struct {
	mu    sync.Mutex
	refs  map[string]Image
	blobs map[string][]byte
	// pinned are the index digests Pin set, by key.
	pinned map[string]bool
	// Corrupt serves other bytes for this blob digest.
	Corrupt string
	// BlobGets counts blob reads.
	BlobGets int
	// Signatures lists digests with a cosign signature.
	Signatures map[string]bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{refs: map[string]Image{}, blobs: map[string][]byte{}, pinned: map[string]bool{}, Signatures: map[string]bool{}}
}

// Tag points ref (as a harness file names it) at img.
func (r *Registry) Tag(ref string, img Image) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pr, err := images.ParseReference(ref)
	if err != nil {
		panic(err)
	}
	r.refs[pr.Registry+"/"+pr.Repo+":"+pr.Tag] = img
	r.refs[pr.Registry+"/"+pr.Repo+"@"+img.Digest()] = img
	r.blobs[digest(img.Config)] = img.Config
	for _, l := range img.Layers {
		r.blobs[digest(l)] = l
	}
}

// Pin serves img for ref, which pins an index digest (as harness.EdgeImage
// does): Resolve answers img's platform digest through that index.
func (r *Registry) Pin(ref string, img Image) {
	pr, err := images.ParseReference(ref)
	if err != nil || pr.Digest == "" {
		panic(fmt.Sprintf("Pin needs a ref pinned by digest: %q", ref))
	}
	tag := pr.Tag
	if tag == "" {
		tag = "pinned"
	}
	r.Tag(pr.Registry+"/"+pr.Repo+":"+tag, img)
	r.mu.Lock()
	defer r.mu.Unlock()
	key := pr.Registry + "/" + pr.Repo + "@" + pr.Digest
	r.refs[key] = img
	r.pinned[key] = true
}

// Resolve answers a tagged or pinned ref.
func (r *Registry) Resolve(_ context.Context, ref string, p images.Platform) (images.Resolved, error) {
	pr, err := images.ParseReference(ref)
	if err != nil {
		return images.Resolved{}, err
	}
	key := pr.Registry + "/" + pr.Repo + ":" + pr.Tag
	if pr.Digest != "" {
		key = pr.Registry + "/" + pr.Repo + "@" + pr.Digest
	}
	r.mu.Lock()
	img, ok := r.refs[key]
	pinned := r.pinned[key]
	r.mu.Unlock()
	if !ok {
		return images.Resolved{}, fmt.Errorf("%s: manifest unknown", ref)
	}
	var doc struct {
		Config images.Descriptor   `json:"config"`
		Layers []images.Descriptor `json:"layers"`
	}
	_ = json.Unmarshal(img.Manifest, &doc)
	out := images.Resolved{Ref: pr, Digest: img.Digest(), MediaType: images.MediaOCIManifest, Manifest: img.Manifest,
		Config: doc.Config, Layers: doc.Layers, Platform: p.String()}
	if pinned {
		out.IndexDigest = pr.Digest
	}
	return out, nil
}

// Blob serves a blob, corrupted when asked.
func (r *Registry) Blob(_ context.Context, _ images.Reference, d string) (io.ReadCloser, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.BlobGets++
	b, ok := r.blobs[d]
	if !ok {
		return nil, errors.New("blob unknown")
	}
	if d == r.Corrupt {
		b = append([]byte{0xff}, b[1:]...)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// Signed reports a cosign signature.
func (r *Registry) Signed(_ context.Context, _ images.Reference, d string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Signatures[d], nil
}

// Scanner answers each stage from Results, keyed by stage; an Err fails it.
type Scanner struct {
	mu      sync.Mutex
	Results map[string]images.StageResult
	Errs    map[string]error
	Calls   []images.ScanRequest
}

// Scan records the request and answers the stage's result.
func (s *Scanner) Scan(_ context.Context, req images.ScanRequest) (images.StageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls = append(s.Calls, req)
	if err := s.Errs[req.Stage]; err != nil {
		return images.StageResult{}, err
	}
	r := s.Results[req.Stage]
	r.Stage = req.Stage
	return r, nil
}

// Set sets one stage's result.
func (s *Scanner) Set(stage string, r images.StageResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Results == nil {
		s.Results = map[string]images.StageResult{}
	}
	s.Results[stage] = r
}

// Loader is a Docker engine that loads layouts.
type Loader struct {
	mu sync.Mutex
	// IDs maps a local name or ID to the ID the engine holds.
	IDs map[string]string
	// LoadID, when set, is the ID every load answers (a mismatch).
	LoadID string
	Loads  int
}

// NewLoader returns an empty engine.
func NewLoader() *Loader { return &Loader{IDs: map[string]string{}} }

// Load answers the platform digest as the containerd store does.
func (l *Loader) Load(_ context.Context, dir, d string) (string, error) {
	if _, err := images.Verify(dir, d); err != nil {
		return "", err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Loads++
	id := d
	if l.LoadID != "" {
		id = l.LoadID
	}
	l.IDs[images.LocalName(d)] = id
	l.IDs[id] = id
	return id, nil
}

// Present answers a loaded image.
func (l *Loader) Present(_ context.Context, ref string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if id, ok := l.IDs[ref]; ok {
		return id, nil
	}
	return "", images.ErrImageGone
}

// Remove forgets an image.
func (l *Loader) Remove(_ context.Context, ref string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.IDs, ref)
	return nil
}

// Notifier records what the vetter told it.
type Notifier struct {
	mu       sync.Mutex
	Decided  []images.Record
	Joined   []string
	Approved []images.Record
	Stale    []images.Record
}

// OnDecided records a decision.
func (n *Notifier) OnDecided(r images.Record) {
	n.mu.Lock()
	n.Decided = append(n.Decided, r)
	n.mu.Unlock()
}

// Funcs adapts the notifier to images.Notifier.
func (n *Notifier) Funcs() images.Notifier { return notifier{n} }

type notifier struct{ n *Notifier }

func (w notifier) Decided(r images.Record) { w.n.OnDecided(r) }
func (w notifier) Joined(_ images.Record, s string) {
	w.n.mu.Lock()
	w.n.Joined = append(w.n.Joined, s)
	w.n.mu.Unlock()
}

func (w notifier) Approved(r images.Record) {
	w.n.mu.Lock()
	w.n.Approved = append(w.n.Approved, r)
	w.n.mu.Unlock()
}

func (w notifier) Stale(r images.Record, _ []string) {
	w.n.mu.Lock()
	w.n.Stale = append(w.n.Stale, r)
	w.n.mu.Unlock()
}

// Reviewer answers a fixed verdict from an independent reviewer.
type Reviewer struct{ Verdict string }

// Review answers the verdict.
func (r Reviewer) Review(context.Context, images.Record, string) (images.ReviewResult, error) {
	return images.ReviewResult{Verdict: r.Verdict, Agent: "fake", Independent: true}, nil
}
