package config

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// Artifact settings are the host's: a laptop push never carries them.
func TestMarshalServeConfigLeavesOutArtifacts(t *testing.T) {
	cfg := &Config{Artifacts: ArtifactsConfig{Host: "aiman.fyi", AccountID: "acct", Bucket: "aiman-artifacts"}}
	data, err := cfg.MarshalServeConfig("h")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "artifacts") || strings.Contains(string(data), "aiman.fyi") {
		t.Fatalf("push must not carry artifact settings:\n%s", data)
	}
	if (ArtifactsConfig{Host: "aiman.fyi", Bucket: "b"}).Configured() {
		t.Fatal("configured without an account id")
	}
}

func TestHostArtifactsFile(t *testing.T) {
	home := t.TempDir()
	fallback := ArtifactsConfig{Host: "old", AccountID: "x", Bucket: "y"}
	got, err := HostArtifacts(home, fallback)
	if err != nil || got.Host != "old" {
		t.Fatalf("no file: %+v %v", got, err)
	}
	want := ArtifactsConfig{Host: "aiman.fyi", AccountID: "acct", Bucket: "aiman-artifacts", Owners: []string{"a@b.com"}, Auth: ArtifactsAuthCFOAuth}
	if err := SaveHostArtifacts(home, want); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(HostArtifactsPath(home))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode: %v %v", fi, err)
	}
	got, err = HostArtifacts(home, fallback)
	if err != nil || got.Host != "aiman.fyi" || got.Auth != ArtifactsAuthCFOAuth || !slices.Equal(got.Owners, want.Owners) {
		t.Fatalf("host file: %+v %v", got, err)
	}
}
