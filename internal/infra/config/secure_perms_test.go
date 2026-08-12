package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecureConfigFile(t *testing.T) {
	tests := []struct {
		name         string
		mode         os.FileMode
		wantChanged  bool
		wantFinal    os.FileMode
		describeWhat string
	}{
		{
			// The case actually observed in the wild: a plaintext API token in a
			// world-readable file.
			name:        "world-readable is tightened",
			mode:        0644,
			wantChanged: true,
			wantFinal:   0600,
		},
		{
			name:        "group-readable is tightened",
			mode:        0640,
			wantChanged: true,
			wantFinal:   0600,
		},
		{
			name:        "world-writable is tightened",
			mode:        0666,
			wantChanged: true,
			wantFinal:   0600,
		},
		{
			name:        "already owner-only is left alone",
			mode:        0600,
			wantChanged: false,
			wantFinal:   0600,
		},
		{
			// Tighter than required is still owner-only; do not loosen it.
			name:        "read-only owner-only is not loosened",
			mode:        0400,
			wantChanged: false,
			wantFinal:   0400,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ConfigName)
			if err := os.WriteFile(path, []byte("integrations: {}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tt.mode); err != nil {
				t.Fatal(err)
			}

			changed, err := SecureConfigFile(path)
			if err != nil {
				t.Fatalf("SecureConfigFile: %v", err)
			}
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}

			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != tt.wantFinal {
				t.Errorf("final mode = %#o, want %#o", got, tt.wantFinal)
			}
		})
	}
}

func TestSecureConfigFileMissingIsNotAnError(t *testing.T) {
	changed, err := SecureConfigFile(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("expected no error for a missing file, got %v", err)
	}
	if changed {
		t.Error("expected changed=false for a missing file")
	}
}

// Repairing must not corrupt or truncate the file it is protecting.
func TestSecureConfigFilePreservesContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigName)
	content := "integrations:\n  jira:\n    url: https://example.atlassian.net\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := SecureConfigFile(path); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("content changed:\ngot  %q\nwant %q", got, content)
	}
}

// Load must repair the file it just read and report having done so.
func TestLoadTightensAndReportsPermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	aimanDir := filepath.Join(dir, DirName)
	if err := os.MkdirAll(aimanDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(aimanDir, ConfigName)
	if err := os.WriteFile(path, []byte("active_remote: devbox\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.PermissionsTightened {
		t.Error("expected PermissionsTightened to be reported")
	}
	if cfg.PermissionsError != nil {
		t.Errorf("unexpected PermissionsError: %v", cfg.PermissionsError)
	}
	if cfg.ActiveRemote != "devbox" {
		t.Errorf("config did not parse: ActiveRemote = %q", cfg.ActiveRemote)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("mode after Load = %#o, want 0600", got)
	}

	// A second load has nothing left to fix.
	cfg2, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.PermissionsTightened {
		t.Error("second Load should report nothing to tighten")
	}
}
