package images

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// scannerBuildTimeout bounds a scanner image build (downloads included).
const scannerBuildTimeout = 30 * time.Minute

// ScannerImage builds the scanner image on the host from aiman's pinned
// Dockerfile and build args, once per distinct input, and answers its ID.
type ScannerImage struct {
	Docker     DockerCLI
	Dockerfile []byte
	// BuildArgs override the Dockerfile's pinned versions and checksums
	// (harness.images.scanners).
	BuildArgs map[string]string
	// Dir holds the build context.
	Dir string

	mu sync.Mutex
	id string
}

// Tag is the image's local name: a hash of the Dockerfile and build args.
func (s *ScannerImage) Tag() string {
	h := sha256.New()
	h.Write(s.Dockerfile)
	keys := make([]string, 0, len(s.BuildArgs))
	for k := range s.BuildArgs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "\n%s=%s", k, s.BuildArgs[k])
	}
	return "aiman-harness-scanner:" + hex.EncodeToString(h.Sum(nil))[:12]
}

// Ensure returns the image ID, building the image when the engine lacks it.
// A build needs a network; a failed build fails the vetting that asked.
func (s *ScannerImage) Ensure(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tag := s.Tag()
	if out, code, err := s.Docker(ctx, []string{"image", "inspect", "--format", "{{.Id}}", tag}); err == nil && code == 0 {
		s.id = strings.TrimSpace(out)
		return s.id, nil
	}
	dir := filepath.Join(s.Dir, "scanner-build")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), s.Dockerfile, 0o600); err != nil {
		return "", err
	}
	args := []string{"build", "--quiet", "--tag", tag}
	keys := make([]string, 0, len(s.BuildArgs))
	for k := range s.BuildArgs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		args = append(args, "--build-arg", k+"="+s.BuildArgs[k])
	}
	bctx, cancel := context.WithTimeout(ctx, scannerBuildTimeout)
	defer cancel()
	out, code, err := s.Docker(bctx, append(args, dir))
	if err != nil || code != 0 {
		return "", toolErr("docker build of the scanner image", []byte(out), code, err)
	}
	out, code, err = s.Docker(ctx, []string{"image", "inspect", "--format", "{{.Id}}", tag})
	if err != nil || code != 0 {
		return "", toolErr("inspect the scanner image", []byte(out), code, err)
	}
	s.id = strings.TrimSpace(out)
	return s.id, nil
}
