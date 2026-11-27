package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ArtifactsFile is the host's own artifact settings, beside config.yaml. Only
// the host's serve publishes artifacts, so these settings live on the host:
// the laptop never holds them and a config push never writes this file.
const ArtifactsFile = "artifacts.yaml"

// HostArtifactsPath is ~/.aiman/artifacts.yaml under home.
func HostArtifactsPath(home string) string {
	return filepath.Join(home, DirName, ArtifactsFile)
}

// LoadHostArtifacts reads the host's artifact settings. A missing file is the
// zero value and no error.
func LoadHostArtifacts(home string) (ArtifactsConfig, error) {
	var a ArtifactsConfig
	b, err := os.ReadFile(HostArtifactsPath(home))
	if os.IsNotExist(err) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	if err := yaml.Unmarshal(b, &a); err != nil {
		return a, fmt.Errorf("%s: %w", HostArtifactsPath(home), err)
	}
	return a, nil
}

// HostArtifacts is the host file's settings when they are configured, else
// fallback (an artifacts block written into config.yaml by hand).
func HostArtifacts(home string, fallback ArtifactsConfig) (ArtifactsConfig, error) {
	a, err := LoadHostArtifacts(home)
	if err != nil {
		return fallback, err
	}
	if a.Configured() {
		return a, nil
	}
	return fallback, nil
}

// SaveHostArtifacts writes the host's artifact settings, mode 0600, through a
// temporary file and a rename.
func SaveHostArtifacts(home string, a ArtifactsConfig) error {
	path := HostArtifactsPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := yaml.Marshal(a)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".artifacts-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
