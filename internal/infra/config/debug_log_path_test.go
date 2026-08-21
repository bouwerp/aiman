package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGetDebugLogPath(t *testing.T) {
	p, err := GetDebugLogPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, filepath.Join(DirName, DebugLogName)) {
		t.Fatalf("got %s", p)
	}
}
