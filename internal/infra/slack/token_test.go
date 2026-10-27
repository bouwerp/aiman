package slack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTokenPathHonoursEnv(t *testing.T) {
	t.Setenv("AIMAN_SLACK_TOKEN_FILE", "/tmp/custom.json")
	if got := TokenPath("/x"); got != "/tmp/custom.json" {
		t.Fatalf("got %q", got)
	}
	if got := TokenPath("/x"); got != "/tmp/custom.json" {
		t.Fatalf("got %q", got)
	}
	_ = filepath.Join
}

func TestTokenPathDefault(t *testing.T) {
	os.Unsetenv("AIMAN_SLACK_TOKEN_FILE")
	if got := TokenPath("/home/u/.aiman"); got != filepath.Join("/home/u/.aiman", "slack-oauth.json") {
		t.Fatalf("got %q", got)
	}
}
