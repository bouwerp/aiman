package awsdelegation

import (
	"context"
	"strings"
	"testing"
)

type mockRemote struct {
	home   string
	files  map[string]string
	execOK bool
}

func (m *mockRemote) Execute(_ context.Context, cmd string) (string, error) {
	if strings.Contains(cmd, `printf %s "$HOME"`) {
		return m.home, nil
	}
	if strings.Contains(cmd, `mkdir -p "$HOME/.aws"`) {
		m.execOK = true
		return "", nil
	}
	if strings.Contains(cmd, `cat "$HOME/.aws/config"`) {
		return m.files["config"], nil
	}
	return "", nil
}

func (m *mockRemote) WriteFile(_ context.Context, path string, content []byte) error {
	if m.files == nil {
		m.files = make(map[string]string)
	}
	rel := strings.TrimPrefix(path, m.home)
	m.files[rel] = string(content)
	return nil
}

func TestApplyDelegatedProfile(t *testing.T) {
	m := &mockRemote{home: "/home/dev", files: map[string]string{"config": "[default]\nregion = us-east-1\n"}}
	err := ApplyDelegatedProfile(context.Background(), m, "delegated-access",
		"arn:aws:iam::1:role/R", "base")
	if err != nil {
		t.Fatal(err)
	}
	written := m.files["/.aws/config"]
	if !strings.Contains(written, "[profile delegated-access]") {
		t.Fatal(written)
	}
	if !m.execOK {
		t.Fatal("expected mkdir")
	}
}

func TestApplyDelegatedProfile_UsesHomePath(t *testing.T) {
	m := &mockRemote{home: "/home/x", files: map[string]string{}}
	_ = ApplyDelegatedProfile(context.Background(), m, "p", "arn:a", "s")
	// WriteFile path should be under home
	if got := m.files["/.aws/config"]; !strings.Contains(got, "role_arn") {
		t.Fatal(got)
	}
}
