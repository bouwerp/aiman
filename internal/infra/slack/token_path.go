package slack

import (
	"os"
	"path/filepath"
	"strings"
)

// TokenPath returns the path to the Slack OAuth credentials file.
// AIMAN_SLACK_TOKEN_FILE overrides the default; otherwise it is
// slack-oauth.json under aimanDir.
func TokenPath(aimanDir string) string {
	if p := strings.TrimSpace(os.Getenv("AIMAN_SLACK_TOKEN_FILE")); p != "" {
		return p
	}
	return filepath.Join(aimanDir, "slack-oauth.json")
}
