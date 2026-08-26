package usecase

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// The session creator and the session itself are usually different machines:
// the TUI runs on the laptop, the session runs on the remote. So the runtime
// env must never carry a path resolved locally.
//
// It used to carry both AIMAN_SOCKET_PATH (from config.GetDir()) and
// AIMAN_BIN_PATH (from os.Executable()), so a session on regent0 was told the
// agent API lived at /Users/pieter/.aiman/aiman.sock — a laptop-only path.
// Every in-session `aiman session ...` call then reported server_not_running
// even though serve was healthy on /home/code/.aiman/aiman.sock, which made
// the agent API unusable from inside a session.
func TestSessionRuntimeEnvCarriesNoLocallyResolvedPaths(t *testing.T) {
	env := SessionRuntimeEnv(&domain.Session{
		ID: "sid", Name: "feat", Group: "work",
	})

	for _, key := range []string{"AIMAN_SOCKET_PATH", "AIMAN_BIN_PATH"} {
		if v, ok := env[key]; ok {
			t.Errorf("%s must not be injected (would be the creator's path, not the session's): %q", key, v)
		}
	}

	// The identity vars are machine-independent and must still be present:
	// the hook reporter exits early without them.
	for key, want := range map[string]string{
		"AIMAN_ENV":          "1",
		"AIMAN_ID":           "sid",
		"AIMAN_SESSION_ID":   "sid",
		"AIMAN_SESSION_NAME": "feat",
		"AIMAN_GROUP":        "work",
	} {
		if env[key] != want {
			t.Errorf("env[%s] = %q, want %q", key, env[key], want)
		}
	}
}
