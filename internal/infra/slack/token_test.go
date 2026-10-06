package slack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreCredentialsMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slack-oauth.json")
	if err := SaveClientID(path, "123.456\n"); err != nil {
		t.Fatal(err)
	}
	if err := Store(path, Credentials{ClientID: "123.456", AccessToken: "xoxp-abc"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	got, err := ReadToken(path)
	if err != nil || got != "xoxp-abc" {
		t.Fatalf("stored %q %v", got, err)
	}
}

func TestReadTokenEnvWinsAndFileIsFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slack-oauth.json")
	if err := Store(path, Credentials{AccessToken: "xoxp-file"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIMAN_SLACK_USER_TOKEN", "xoxp-env")
	got, err := ReadToken(path)
	if err != nil || got != "xoxp-env" {
		t.Fatalf("env %q %v", got, err)
	}
	t.Setenv("AIMAN_SLACK_USER_TOKEN", "")
	got, err = ReadToken(path)
	if err != nil || got != "xoxp-file" {
		t.Fatalf("file %q %v", got, err)
	}
}

func TestStoreRejectsBotToken(t *testing.T) {
	err := Store(filepath.Join(t.TempDir(), "t"), Credentials{AccessToken: "xoxb-nope"})
	if err == nil || strings.Contains(err.Error(), "xoxb-nope") {
		t.Fatalf("err %v", err)
	}
	err = SaveClientID(filepath.Join(t.TempDir(), "t"), "  \n")
	if err == nil {
		t.Fatal("blank client id")
	}
}
