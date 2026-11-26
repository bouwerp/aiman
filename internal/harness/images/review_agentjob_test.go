package images

import (
	"slices"
	"testing"

	"github.com/bouwerp/aiman/internal/agentjob"
)

// agentjob starts this package's review proxy, so the proxy's contract must
// match on both sides.
func TestAgentJobMatchesTheReviewProxy(t *testing.T) {
	if agentjob.ProxyPort != ProxyPort || agentjob.ProxyReadyLine != ProxyReadyLine || agentjob.ContainerAiman != ContainerAiman {
		t.Fatalf("agentjob proxy contract (%d, %q, %q) differs from the review proxy's (%d, %q, %q)",
			agentjob.ProxyPort, agentjob.ProxyReadyLine, agentjob.ContainerAiman, ProxyPort, ProxyReadyLine, ContainerAiman)
	}
}

// The review container works in the bundle: docker's workdir and Codex's -C.
func TestReviewContainerWorksInTheBundle(t *testing.T) {
	m := &ModelReviewer{o: ReviewOptions{UID: 1, GID: 1, AimanBin: "/aiman"}}
	p := pick{bin: "/usr/bin/codex", cred: Credential{Mode: CredentialAPIKey}}
	p.entry.Agent = "codex"
	c, err := m.container(t.TempDir(), "img", Record{Digest: "sha256:abc"}, p, BundleInfo{Dir: "/b"})
	if err != nil {
		t.Fatal(err)
	}
	args := ReviewArgs(c)
	if i := slices.Index(args, "--workdir"); i < 0 || args[i+1] != reviewBundle {
		t.Errorf("args %q lack --workdir %s", args, reviewBundle)
	}
	if i := slices.Index(c.Entrypoint, "-C"); i < 0 || c.Entrypoint[i+1] != reviewBundle {
		t.Errorf("entrypoint %q lacks -C %s", c.Entrypoint, reviewBundle)
	}
	if c.Mounts[0].Host != "/b" || c.Mounts[0].Container != reviewBundle || !slices.Equal(c.Allow, []string{"api.openai.com:443"}) {
		t.Errorf("container = %+v", c)
	}
}
