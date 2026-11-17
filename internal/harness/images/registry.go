package images

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Media types serve reads.
const (
	MediaOCIIndex        = "application/vnd.oci.image.index.v1+json"
	MediaOCIManifest     = "application/vnd.oci.image.manifest.v1+json"
	MediaDockerList      = "application/vnd.docker.distribution.manifest.list.v2+json"
	MediaDockerManifest  = "application/vnd.docker.distribution.manifest.v2+json"
	maxManifestBytes     = 4 << 20
	registryCallTimeout  = 30 * time.Second
	registryTokenTimeout = 15 * time.Second
)

var manifestAccept = strings.Join([]string{MediaOCIIndex, MediaDockerList, MediaOCIManifest, MediaDockerManifest}, ", ")

// Descriptor names a blob.
type Descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Platform    *Platform         `json:"platform,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Platform is an index entry's platform.
type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}

func (p Platform) String() string {
	s := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// HostPlatform is the platform of this host's engine.
func HostPlatform() Platform {
	p := Platform{OS: "linux", Architecture: runtime.GOARCH}
	if runtime.GOARCH == "arm64" {
		p.Variant = "v8"
	}
	return p
}

type manifestDoc struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Manifests     []Descriptor `json:"manifests"`
	Config        Descriptor   `json:"config"`
	Layers        []Descriptor `json:"layers"`
}

// Resolved is a ref resolved to one platform's manifest.
type Resolved struct {
	Ref Reference
	// Digest is the platform manifest's digest; IndexDigest the index it was
	// picked from, empty for a single-platform ref.
	Digest, IndexDigest string
	MediaType           string
	Manifest            []byte
	Config              Descriptor
	Layers              []Descriptor
	Platform            string
}

// Size is the config and layer sizes the manifest declares.
func (r Resolved) Size() int64 {
	n := r.Config.Size
	for _, l := range r.Layers {
		n += l.Size
	}
	return n
}

// Registry reads manifests and blobs.
type Registry interface {
	Resolve(ctx context.Context, ref string, platform Platform) (Resolved, error)
	Blob(ctx context.Context, ref Reference, digest string) (io.ReadCloser, error)
	// Signed reports whether a cosign signature manifest exists for digest.
	Signed(ctx context.Context, ref Reference, digest string) (bool, error)
}

// HTTPRegistry speaks the registry HTTP API v2 with anonymous or basic
// credentials and bearer tokens. It reads credentials from the "auths" of
// ~/.docker/config.json; credential helpers are not used.
type HTTPRegistry struct {
	Client *http.Client
	// PlainHTTP talks http instead of https (tests).
	PlainHTTP bool
	// DockerConfig is the docker config file; empty is ~/.docker/config.json.
	DockerConfig string

	mu     sync.Mutex
	tokens map[string]string
}

func (r *HTTPRegistry) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (r *HTTPRegistry) url(ref Reference, kind, target string) string {
	scheme := "https"
	if r.PlainHTTP {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s/v2/%s/%s/%s", scheme, ref.Registry, ref.Repo, kind, target)
}

// Resolve reads the ref's manifest and, for an index, the manifest of
// platform. It never downloads a layer.
func (r *HTTPRegistry) Resolve(ctx context.Context, raw string, platform Platform) (Resolved, error) {
	ref, err := ParseReference(raw)
	if err != nil {
		return Resolved{}, err
	}
	body, media, err := r.manifest(ctx, ref, ref.reference())
	if err != nil {
		return Resolved{}, err
	}
	out := Resolved{Ref: ref}
	doc, err := parseManifest(body, media)
	if err != nil {
		return Resolved{}, err
	}
	if isIndex(doc.MediaType) {
		out.IndexDigest = sha256Digest(body)
		pick, ok := pickPlatform(doc.Manifests, platform)
		if !ok {
			return Resolved{}, fmt.Errorf("%s has no manifest for %s", raw, platform)
		}
		if body, media, err = r.manifest(ctx, ref, pick.Digest); err != nil {
			return Resolved{}, err
		}
		if doc, err = parseManifest(body, media); err != nil {
			return Resolved{}, err
		}
		if isIndex(doc.MediaType) {
			return Resolved{}, fmt.Errorf("%s: nested index", raw)
		}
	}
	out.Digest, out.MediaType, out.Manifest = sha256Digest(body), doc.MediaType, body
	out.Config, out.Layers, out.Platform = doc.Config, doc.Layers, platform.String()
	if ref.Digest != "" && out.IndexDigest == "" && out.Digest != ref.Digest {
		return Resolved{}, fmt.Errorf("%s: manifest digest %s does not match", raw, out.Digest)
	}
	if ref.Digest != "" && out.IndexDigest != "" && out.IndexDigest != ref.Digest {
		return Resolved{}, fmt.Errorf("%s: index digest %s does not match", raw, out.IndexDigest)
	}
	return out, nil
}

func parseManifest(body []byte, media string) (manifestDoc, error) {
	var doc manifestDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return doc, fmt.Errorf("read manifest: %w", err)
	}
	if doc.MediaType == "" {
		doc.MediaType = media
	}
	if doc.MediaType == "" && len(doc.Manifests) > 0 {
		doc.MediaType = MediaOCIIndex
	}
	if doc.MediaType == "" && doc.Config.Digest != "" {
		doc.MediaType = MediaOCIManifest
	}
	if doc.SchemaVersion != 2 {
		return doc, fmt.Errorf("manifest schema %d is not supported", doc.SchemaVersion)
	}
	switch doc.MediaType {
	case MediaOCIIndex, MediaDockerList:
	case MediaOCIManifest, MediaDockerManifest:
		if !digestRe.MatchString(doc.Config.Digest) {
			return doc, errors.New("manifest has no sha256 config digest")
		}
		for _, l := range doc.Layers {
			if !digestRe.MatchString(l.Digest) || l.Size < 0 {
				return doc, fmt.Errorf("manifest layer %q is not a sha256 blob", l.Digest)
			}
		}
	default:
		return doc, fmt.Errorf("manifest media type %q is not supported", doc.MediaType)
	}
	return doc, nil
}

func isIndex(media string) bool { return media == MediaOCIIndex || media == MediaDockerList }

func pickPlatform(list []Descriptor, want Platform) (Descriptor, bool) {
	var fallback *Descriptor
	for i, d := range list {
		p := d.Platform
		if p == nil || p.OS != want.OS || p.Architecture != want.Architecture {
			continue
		}
		if p.Variant == want.Variant {
			return d, true
		}
		if fallback == nil && (p.Variant == "" || want.Variant == "") {
			fallback = &list[i]
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return Descriptor{}, false
}

func sha256Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// manifest GETs one manifest and checks a digest target against its bytes.
func (r *HTTPRegistry) manifest(ctx context.Context, ref Reference, target string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, registryCallTimeout)
	defer cancel()
	resp, err := r.do(ctx, ref, http.MethodGet, r.url(ref, "manifests", target), manifestAccept)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("manifest %s@%s: registry answered %s", ref.Repo, target, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxManifestBytes {
		return nil, "", fmt.Errorf("manifest %s@%s is over %d bytes", ref.Repo, target, maxManifestBytes)
	}
	if digestRe.MatchString(target) && sha256Digest(body) != target {
		return nil, "", fmt.Errorf("manifest %s@%s: the registry sent other bytes", ref.Repo, target)
	}
	media, _, _ := strings.Cut(resp.Header.Get("Content-Type"), ";")
	return body, strings.TrimSpace(media), nil
}

// Blob opens one blob. The caller checks its digest while reading.
func (r *HTTPRegistry) Blob(ctx context.Context, ref Reference, digest string) (io.ReadCloser, error) {
	resp, err := r.do(ctx, ref, http.MethodGet, r.url(ref, "blobs", digest), "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("blob %s: registry answered %s", digest, resp.Status)
	}
	return resp.Body, nil
}

// Signed asks for the cosign signature tag sha256-<hex>.sig.
func (r *HTTPRegistry) Signed(ctx context.Context, ref Reference, digest string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, registryCallTimeout)
	defer cancel()
	tag := strings.Replace(digest, ":", "-", 1) + ".sig"
	resp, err := r.do(ctx, ref, http.MethodHead, r.url(ref, "manifests", tag), manifestAccept)
	if err != nil {
		return false, err
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("signature lookup: registry answered %s", resp.Status)
	}
}

// do sends one request, answering a 401 challenge once with a bearer token
// or basic credentials.
func (r *HTTPRegistry) do(ctx context.Context, ref Reference, method, u, accept string) (*http.Response, error) {
	send := func(auth string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, u, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		return r.client().Do(req) //nolint:gosec // G107: the registry URL comes from a parsed image reference
	}
	key := ref.Registry + "/" + ref.Repo
	r.mu.Lock()
	auth := r.tokens[key]
	r.mu.Unlock()
	resp, err := send(auth)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	_ = resp.Body.Close()
	if auth, err = r.authorize(ctx, ref, challenge); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.tokens == nil {
		r.tokens = map[string]string{}
	}
	r.tokens[key] = auth
	r.mu.Unlock()
	return send(auth)
}

func (r *HTTPRegistry) authorize(ctx context.Context, ref Reference, challenge string) (string, error) {
	user, pass := r.credentials(ref.Registry)
	scheme, params := parseChallenge(challenge)
	switch strings.ToLower(scheme) {
	case "basic":
		if user == "" {
			return "", fmt.Errorf("registry %s needs credentials", ref.Registry)
		}
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)), nil
	case "bearer":
	default:
		return "", fmt.Errorf("registry %s: unsupported auth challenge %q", ref.Registry, scheme)
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || (realm.Scheme != "https" && !r.PlainHTTP) {
		return "", fmt.Errorf("registry %s: bad token realm %q", ref.Registry, params["realm"])
	}
	q := realm.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	q.Set("scope", "repository:"+ref.Repo+":pull")
	realm.RawQuery = q.Encode()
	tctx, cancel := context.WithTimeout(ctx, registryTokenTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(tctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := r.client().Do(req) //nolint:gosec // G107: the realm comes from the registry's challenge
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry %s token: %s", ref.Registry, resp.Status)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil {
		return "", fmt.Errorf("registry %s token: %w", ref.Registry, err)
	}
	if tok.Token == "" {
		tok.Token = tok.AccessToken
	}
	if tok.Token == "" {
		return "", fmt.Errorf("registry %s gave no token", ref.Registry)
	}
	return "Bearer " + tok.Token, nil
}

// parseChallenge reads `Bearer realm="…",service="…"`.
func parseChallenge(h string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params := map[string]string{}
	for _, part := range splitParams(rest) {
		k, v, ok := strings.Cut(part, "=")
		if ok {
			params[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return scheme, params
}

// splitParams splits on commas outside quotes.
func splitParams(s string) []string {
	var out []string
	quoted, start := false, 0
	for i, c := range s {
		switch {
		case c == '"':
			quoted = !quoted
		case c == ',' && !quoted:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// credentials reads the base64 "auth" of the registry in the docker config.
func (r *HTTPRegistry) credentials(registry string) (string, string) {
	path := r.DockerConfig
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", ""
		}
		path = filepath.Join(home, ".docker", "config.json")
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: the operator's docker config
	if err != nil {
		return "", ""
	}
	var cfg struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return "", ""
	}
	keys := []string{registry, "https://" + registry}
	if registry == dockerHub {
		keys = append(keys, "https://index.docker.io/v1/", "index.docker.io", "docker.io")
	}
	for _, k := range keys {
		if a, ok := cfg.Auths[k]; ok && a.Auth != "" {
			raw, err := base64.StdEncoding.DecodeString(a.Auth)
			if err != nil {
				continue
			}
			user, pass, _ := strings.Cut(string(raw), ":")
			return user, pass
		}
	}
	return "", ""
}
