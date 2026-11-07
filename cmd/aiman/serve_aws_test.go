package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
)

func TestAWSCredentialsWriterWritesSection(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".aws", "credentials")
	w := awsCredentialsWriter{path: path, lockPath: filepath.Join(home, ".aiman", "aws-credentials.lock")}
	exp := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	err := w.WriteHostCredentials("dev", &awsdelegation.SessionCredentials{
		AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret", SessionToken: "token", Expiration: exp,
	})
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := awsdelegation.ReadHostCredentialProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].Name != "dev" || !profiles[0].ExpiresAt.Equal(exp) {
		t.Errorf("profiles = %+v, want dev expiring %s", profiles, exp)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "[dev]") {
		t.Errorf("file = %q", body)
	}
}

func TestWireAWSLoginNeedsDelegations(t *testing.T) {
	if closeAWS := wireAWSLogin(nil, nil, t.TempDir()); closeAWS != nil {
		t.Error("no delegations must leave the aws methods unwired")
	}
}
