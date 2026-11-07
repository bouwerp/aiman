package transcripteval

import (
	"os"
	"testing"
)

// TestMain clears AIMAN_ID so an operator call made from inside an aiman
// session is not sent as that session's call.
func TestMain(m *testing.M) {
	if err := os.Unsetenv("AIMAN_ID"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
