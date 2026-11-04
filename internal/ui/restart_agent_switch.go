package ui

import (
	"os"
	"path/filepath"

	"github.com/bouwerp/aiman/internal/quality"
)

// laptopChecklistDir is the local user checklist directory pushed to the
// remote, or "" when the home directory is unknown.
func laptopChecklistDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, quality.UserChecklistDir)
}
