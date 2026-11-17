package images_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness/images"
)

// TestReviewContainerCannotReachOtherHost runs the review container with a
// shell in place of the agent. Through the proxy, a host outside the
// allowlist is refused (403) while the allowlisted one is not; without the
// proxy, no address is reachable at all. No model is called.
func TestReviewContainerCannotReachOtherHost(t *testing.T) {
	needDocker(t)
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, "README.txt"), []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := `
connect() { # host port -> first line of the proxy's answer
  exec 3<>/dev/tcp/proxy/3128 || { echo "PROXY_UNREACHABLE"; return; }
  printf 'CONNECT %s:%s HTTP/1.1\r\nHost: %s:%s\r\n\r\n' "$1" "$2" "$1" "$2" >&3
  read -r -t 20 line <&3; echo "$1:$2 -> $line"; exec 3>&-
}
connect example.org 443
connect example.com 443
connect 1.1.1.1 443
if timeout 5 bash -c 'exec 3<>/dev/tcp/1.1.1.1/443' 2>/dev/null; then echo DIRECT_OPEN; else echo DIRECT_BLOCKED; fi
if timeout 5 bash -c 'exec 3<>/dev/tcp/example.com/443' 2>/dev/null; then echo DIRECT_DNS_OPEN; else echo DIRECT_DNS_BLOCKED; fi
cat /bundle/README.txt; echo
if echo x > /bundle/w 2>/dev/null; then echo BUNDLE_WRITABLE; else echo BUNDLE_RO; fi
if echo x > /etc/w 2>/dev/null; then echo ROOT_WRITABLE; else echo ROOT_RO; fi
`
	c := images.ReviewContainer{
		Name: "aiman-review-test-" + strings.ToLower(strings.ReplaceAll(t.Name(), "Test", ""))[:12], Image: scannerImage,
		UID: os.Getuid(), GID: os.Getgid(), AimanBin: aimanBin, Allow: []string{"example.com:443"},
		Mounts:     []images.Mount{{Host: bundle, Container: "/bundle", ReadOnly: true}},
		Entrypoint: []string{"/bin/bash", "-c", probe},
		Timeout:    3 * time.Minute,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := images.DockerReviewRunner{}.Run(ctx, c)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	t.Logf("probe:\n%s", out)
	for _, want := range []string{
		"example.org:443 -> HTTP/1.1 403", "1.1.1.1:443 -> HTTP/1.1 403",
		"DIRECT_BLOCKED", "DIRECT_DNS_BLOCKED", "bundle", "BUNDLE_RO", "ROOT_RO",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("probe lacks %q", want)
		}
	}
	if strings.Contains(out, "example.com:443 -> HTTP/1.1 403") || strings.Contains(out, "PROXY_UNREACHABLE") {
		t.Errorf("the allowlisted host was refused or the proxy was unreachable")
	}
}
