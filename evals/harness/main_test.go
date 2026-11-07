package harness

import (
	"os"
	"testing"
)

// TestMain clears AIMAN_ID so the suite behaves the same inside an aiman
// session: the client sends it as the caller, which would turn every operator
// call in these evals into a call from a session the harness does not know.
func TestMain(m *testing.M) {
	if err := os.Unsetenv("AIMAN_ID"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
