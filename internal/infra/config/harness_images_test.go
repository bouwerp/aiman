package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestHarnessImagesDefaults(t *testing.T) {
	var i HarnessImagesConfig
	if i.CVEBlockOrDefault() != "critical" || i.ScanTimeoutOrDefault() != 30*time.Minute || i.DBRefreshOrDefault() != 24*time.Hour {
		t.Fatalf("defaults: %s %s %s", i.CVEBlockOrDefault(), i.ScanTimeoutOrDefault(), i.DBRefreshOrDefault())
	}
	if v, w := i.MaxSizeBytes(); v != 4<<30 || w != nil {
		t.Fatalf("max size %d %v", v, w)
	}
	if !i.ReviewOn() {
		t.Fatal("review unset is on")
	}
}

func TestHarnessImagesParse(t *testing.T) {
	var h HarnessConfig
	src := `
images:
  cve_block: high
  max_size: 1G
  db_refresh: 1h
  scanners: {TRIVY_VERSION: "0.70.0", EVIL: x}
`
	if err := yaml.Unmarshal([]byte(src), &h); err != nil {
		t.Fatal(err)
	}
	if h.Images.CVEBlockOrDefault() != "high" || h.Images.DBRefreshOrDefault() != 24*time.Hour {
		t.Fatalf("parsed: %+v (db_refresh has a 24h floor)", h.Images)
	}
	if v, _ := h.Images.MaxSizeBytes(); v != 1<<30 {
		t.Fatalf("max size %d", v)
	}
	args, warns := h.Images.ScannerBuildArgs()
	if args["TRIVY_VERSION"] != "0.70.0" || len(args) != 1 || len(warns) != 1 {
		t.Fatalf("build args %v, warnings %v", args, warns)
	}
}
