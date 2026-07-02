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
	cmds   []string
}

func (m *mockRemote) Execute(_ context.Context, cmd string) (string, error) {
	m.cmds = append(m.cmds, cmd)
	if strings.Contains(cmd, `printf %s "$HOME"`) {
		return m.home, nil
	}
	if strings.Contains(cmd, "mkdir -p") && (strings.Contains(cmd, ".aws") || strings.Contains(cmd, ".aiman/aws")) {
		m.execOK = true
		return "", nil
	}
	if strings.Contains(cmd, ".aws/credentials") && strings.Contains(cmd, "cat") {
		return m.files["credentials"], nil
	}
	if strings.Contains(cmd, ".aws/config") && strings.Contains(cmd, "cat") {
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
		"arn:aws:iam::1:role/R", "base", "")
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

func TestApplyDelegatedProfile_WithRegion(t *testing.T) {
	m := &mockRemote{home: "/home/dev", files: map[string]string{}}
	err := ApplyDelegatedProfile(context.Background(), m, "delegated-access",
		"arn:aws:iam::1:role/R", "base", "eu-west-1")
	if err != nil {
		t.Fatal(err)
	}
	written := m.files["/.aws/config"]
	if !strings.Contains(written, "region = eu-west-1") {
		t.Fatalf("expected region line in config, got:\n%s", written)
	}
}

func TestApplyDelegatedProfile_RegionOnly(t *testing.T) {
	m := &mockRemote{home: "/home/dev", files: map[string]string{}}
	err := ApplyDelegatedProfile(context.Background(), m, "default", "", "", "eu-west-1")
	if err != nil {
		t.Fatal(err)
	}
	written := m.files["/.aws/config"]
	if !strings.Contains(written, "[default]") || !strings.Contains(written, "region = eu-west-1") {
		t.Fatalf("expected default region config, got:\n%s", written)
	}
	if strings.Contains(written, "role_arn") {
		t.Fatalf("did not expect role_arn in sync config, got:\n%s", written)
	}
}

func TestApplyDelegatedProfile_UsesHomePath(t *testing.T) {
	m := &mockRemote{home: "/home/x", files: map[string]string{}}
	_ = ApplyDelegatedProfile(context.Background(), m, "p", "arn:a", "s", "")
	// WriteFile path should be under home
	if got := m.files["/.aws/config"]; !strings.Contains(got, "role_arn") {
		t.Fatal(got)
	}
}

func TestApplyDelegatedCredentials_ChmodsFile(t *testing.T) {
	m := &mockRemote{home: "/home/dev", files: map[string]string{}}
	err := ApplyDelegatedCredentials(context.Background(), m, "default", &SessionCredentials{
		AccessKeyID:     "AKIA123",
		SecretAccessKey: "secret",
		SessionToken:    "token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.files["/.aws/credentials"]; !strings.Contains(got, "[default]") || !strings.Contains(got, "aws_session_token = token") {
		t.Fatalf("unexpected credentials file:\n%s", got)
	}
	if !m.execOK {
		t.Fatal("expected .aws dir creation")
	}
	if joined := strings.Join(m.cmds, "\n"); !strings.Contains(joined, `chmod 600 "/home/dev/.aws/credentials"`) {
		t.Fatalf("expected chmod 600 for ~/.aws/credentials, got:\n%s", joined)
	}
}

func TestApplyDelegatedProfile_ChmodsFile(t *testing.T) {
	m := &mockRemote{home: "/home/dev", files: map[string]string{}}
	err := ApplyDelegatedProfile(context.Background(), m, "default", "", "", "eu-west-1")
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(m.cmds, "\n"); !strings.Contains(joined, `chmod 600 "/home/dev/.aws/config"`) {
		t.Fatalf("expected chmod 600 for ~/.aws/config, got:\n%s", joined)
	}
}

func TestRemoveSessionCredentialFiles(t *testing.T) {
	m := &mockRemote{home: "/home/dev", files: map[string]string{}}
	if err := RemoveSessionCredentialFiles(context.Background(), m, "sess-1"); err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(m.cmds, "\n"); !strings.Contains(joined, `rm -rf "/home/dev/.aiman/aws/sess-1"`) {
		t.Fatalf("expected session AWS dir removal, got:\n%s", joined)
	}
}

func TestRemoveAllSessionCredentialFiles(t *testing.T) {
	m := &mockRemote{home: "/home/dev", files: map[string]string{}}
	if err := RemoveAllSessionCredentialFiles(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(m.cmds, "\n"); !strings.Contains(joined, `rm -rf "/home/dev/.aiman/aws"`) {
		t.Fatalf("expected all session AWS dirs removal, got:\n%s", joined)
	}
}

func TestRenameSessionProfile(t *testing.T) {
	m := &mockRemote{home: "/home/dev", files: map[string]string{
		"credentials": "[old]\naws_access_key_id = abc\naws_secret_access_key = def\n",
		"config":      "[profile old]\nrole_arn = arn:aws:iam::1:role/R\nsource_profile = base\n",
	}}
	if err := RenameSessionProfile(context.Background(), m, "old", "new"); err != nil {
		t.Fatal(err)
	}
	if got := m.files["/.aws/credentials"]; !strings.Contains(got, "[new]") || strings.Contains(got, "[old]") {
		t.Fatalf("unexpected renamed credentials:\n%s", got)
	}
	if got := m.files["/.aws/config"]; !strings.Contains(got, "[profile new]") || strings.Contains(got, "[profile old]") {
		t.Fatalf("unexpected renamed config:\n%s", got)
	}
}


func TestRenameSessionProfileRefusesOverwrite(t *testing.T) {
	m := &mockRemote{home: "/home/dev", files: map[string]string{
		"credentials": "[old]\naws_access_key_id = abc\n\n[new]\naws_access_key_id = xyz\n",
		"config":      "[profile old]\nrole_arn = arn:aws:iam::1:role/R\n\n[profile new]\nrole_arn = arn:aws:iam::1:role/R2\n",
	}}
	if err := RenameSessionProfile(context.Background(), m, "old", "new"); err == nil {
		t.Fatal("expected rename collision error")
	}
}
