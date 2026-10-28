package usecase

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// piTrustRemote answers the single probe EnsurePiWorkspaceTrusted runs.
type piTrustRemote struct {
	probe   string
	cmds    []string
	written map[string][]byte
}

func (r *piTrustRemote) Execute(_ context.Context, cmd string) (string, error) {
	r.cmds = append(r.cmds, cmd)
	if strings.Contains(cmd, "trust.json") {
		return r.probe, nil
	}
	return "", nil
}

func (r *piTrustRemote) WriteFile(_ context.Context, path string, content []byte) error {
	if r.written == nil {
		r.written = map[string][]byte{}
	}
	r.written[path] = content
	return nil
}

func TestEnsurePiWorkspaceTrustedMergesTrustJSON(t *testing.T) {
	r := &piTrustRemote{probe: "/home/code\npi\n{\n  \"/keep\": true,\n  \"/denied\": false\n}\n"}
	if err := EnsurePiWorkspaceTrusted(context.Background(), r, "/w/app@feat", "/denied"); err != nil {
		t.Fatal(err)
	}
	body, ok := r.written["/home/code/.pi/agent/trust.json"]
	if !ok {
		t.Fatalf("no trust.json write: %#v", r.written)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if got["/keep"] != true || got["/w/app@feat"] != true {
		t.Fatalf("trust.json %s", body)
	}
	// "Do not trust" is the user's decision; aiman never flips it.
	if got["/denied"] != false {
		t.Fatalf("overrode a deny: %s", body)
	}
}

func TestEnsurePiWorkspaceTrustedSkipsHostWithoutPi(t *testing.T) {
	r := &piTrustRemote{probe: "/home/code\n"}
	if err := EnsurePiWorkspaceTrusted(context.Background(), r, "/w"); err != nil {
		t.Fatal(err)
	}
	if len(r.written) != 0 {
		t.Fatalf("wrote %#v on a host without ~/.pi/agent", r.written)
	}
}

func TestEnsurePiWorkspaceTrustedLeavesUnreadableStore(t *testing.T) {
	r := &piTrustRemote{probe: "/home/code\npi\n[not json\n"}
	if err := EnsurePiWorkspaceTrusted(context.Background(), r, "/w"); err == nil {
		t.Fatal("want an error for a trust store Pi itself would reject")
	}
	if len(r.written) != 0 {
		t.Fatalf("clobbered an unreadable trust store: %#v", r.written)
	}
}

func TestEnsurePiWorkspaceTrustedSkipsWriteWhenAlreadyTrusted(t *testing.T) {
	r := &piTrustRemote{probe: "/home/code\npi\n{\"/w\": true}\n"}
	if err := EnsurePiWorkspaceTrusted(context.Background(), r, "/w"); err != nil {
		t.Fatal(err)
	}
	if len(r.written) != 0 {
		t.Fatalf("rewrote an already-trusted store: %#v", r.written)
	}
}

func TestTrustWorkspacePathsTrustsPi(t *testing.T) {
	r := &piTrustRemote{probe: "/home/code\npi\n"}
	TrustWorkspacePaths(context.Background(), r, "/w/app@feat/svc", "/w/app@feat")
	body := string(r.written["/home/code/.pi/agent/trust.json"])
	if !strings.Contains(body, `"/w/app@feat": true`) || !strings.Contains(body, `"/w/app@feat/svc": true`) {
		t.Fatalf("pi trust.json %q", body)
	}
}
