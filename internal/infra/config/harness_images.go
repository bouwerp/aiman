package config

import (
	"fmt"
	"time"
)

// Defaults of harness.images.
const (
	DefaultHarnessImageMaxSize     = "4G"
	DefaultHarnessImageScanTimeout = 30 * time.Minute
	DefaultHarnessImageDBRefresh   = 24 * time.Hour
)

// scannerBuildArgs are the build args of build/harness-scanner/Dockerfile
// harness.images.scanners may override.
var scannerBuildArgs = map[string]bool{
	"BASE": true, "TRIVY_VERSION": true, "TRIVY_SHA256_AMD64": true, "TRIVY_SHA256_ARM64": true,
	"OSV_VERSION": true, "OSV_SHA256_AMD64": true, "OSV_SHA256_ARM64": true,
	"YARA_FORGE_RELEASE": true, "YARA_FORGE_SHA256": true, "CLAMAV_VERSION": true, "YARA_VERSION": true,
}

// HarnessImagesConfig is harness.images: how serve vets the images a run
// names before the Docker engine sees them.
type HarnessImagesConfig struct {
	// Scanners overrides the scanner image's pinned versions and checksums,
	// as Dockerfile build args (TRIVY_VERSION, TRIVY_SHA256_AMD64, ...).
	Scanners map[string]string `yaml:"scanners,omitempty"`
	// CVEBlock is critical (default) or high: a CVE at or above it blocks.
	CVEBlock string `yaml:"cve_block,omitempty"`
	// Review turns the model review on; unset is on. Off, no image is
	// approved without a question.
	Review *bool `yaml:"review,omitempty"`
	// MaxSize caps an image's compressed layer sum (4G).
	MaxSize string `yaml:"max_size,omitempty"`
	// ScanTimeout bounds a whole vetting (30m).
	ScanTimeout time.Duration `yaml:"scan_timeout,omitempty"`
	// DBRefresh is how often the scanner databases refresh (24h, the floor).
	DBRefresh time.Duration `yaml:"db_refresh,omitempty"`
}

// CVEBlockOrDefault is cve_block, critical unless it says high.
func (i HarnessImagesConfig) CVEBlockOrDefault() string {
	if i.CVEBlock == "high" {
		return "high"
	}
	return "critical"
}

// ReviewOn reports whether the model review runs; unset is on.
func (i HarnessImagesConfig) ReviewOn() bool { return i.Review == nil || *i.Review }

// MaxSizeBytes is max_size in bytes; an unreadable value keeps the default.
func (i HarnessImagesConfig) MaxSizeBytes() (int64, []string) {
	return harnessSize("images.max_size", i.MaxSize, DefaultHarnessImageMaxSize)
}

// ScanTimeoutOrDefault is scan_timeout, or 30m.
func (i HarnessImagesConfig) ScanTimeoutOrDefault() time.Duration {
	if i.ScanTimeout > 0 {
		return i.ScanTimeout
	}
	return DefaultHarnessImageScanTimeout
}

// DBRefreshOrDefault is db_refresh, never under 24h.
func (i HarnessImagesConfig) DBRefreshOrDefault() time.Duration {
	return max(i.DBRefresh, DefaultHarnessImageDBRefresh)
}

// ScannerBuildArgs are the known build-arg overrides; an unknown key is
// dropped and named in a warning.
func (i HarnessImagesConfig) ScannerBuildArgs() (map[string]string, []string) {
	out := map[string]string{}
	var warns []string
	for k, v := range i.Scanners {
		if !scannerBuildArgs[k] {
			warns = append(warns, fmt.Sprintf("harness.images.scanners.%s is not a scanner build arg; ignored", k))
			continue
		}
		out[k] = v
	}
	return out, warns
}
