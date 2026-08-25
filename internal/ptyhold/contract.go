// Package ptyhold implements the durable holder contract for built-in PTY
// sessions: a tiny, deliberately frozen process that owns a PTY so sessions
// survive aiman serve restarts and updates.
//
// # The contract (v1 — intended permanent)
//
// All state lives in one directory per session, $AIMAN_ROOT/pty/<id>/:
//
//	request.json  written by serve before spawning; the full spec
//	meta.json     written by the holder at startup (id,name,dir,pid,...),
//	              removed on exit
//	spool         append-only raw PTY output; history is FILES, readable by
//	spool.old     anything. On exceeding SpoolMaxBytes the active file is
//	              renamed to spool.old (replacing it) and a fresh spool starts.
//	term.sock     RAW bidirectional byte pipe into the live PTY. Live output
//	              only — replay comes from the spool files. No framing.
//	resize        serve writes "COLSxROWS\n"; holder applies TIOCSWINSZ and
//	              deletes the file
//	kill          serve touches this file; holder SIGTERM->grace->SIGKILLs the
//	              child and exits cleanly
//	exit          holder writes its final status here on the way out
//
// Design rules that keep updates harmless:
//   - The holder speaks no protocol: one raw socket, flat files.
//   - The holder holds no references to serve and never calls back into it.
//   - Unknown files in the session dir are ignored by both sides.
//   - AIMAN_PTY_SPOOL_MAX overrides the spool segment size (the one env knob;
//     production default is 8 MiB).
//   - Serve may crash, be upgraded, or be down for hours; holders keep
//     running and spooling regardless.
package ptyhold

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	// SpoolMaxBytes caps the active spool segment before rotation (8 MiB).
	SpoolMaxBytes = 8 << 20

	// MetaFile / SpoolFile etc. are the contract's fixed file names.
	MetaFile    = "meta.json"
	RequestFile = "request.json"
	SpoolFile   = "spool"
	SpoolOld    = "spool.old"
	SocketFile  = "term.sock"
	ResizeFile  = "resize"
	KillFile    = "kill"
	ExitFile    = "exit"
)

// Spec is what serve hands to a holder. Field names are part of the durable
// contract: never rename or repurpose, only ever add.
type Spec struct {
	ID      string            `json:"id"`
	Name    string            `json:"name,omitempty"`
	Dir     string            `json:"dir"`
	Command string            `json:"command"`
	Env     map[string]string `json:"env,omitempty"`
	Cols    int               `json:"cols,omitempty"`
	Rows    int               `json:"rows,omitempty"`
}

// Dir returns the session directory for id under root.
func Dir(root, id string) string { return filepath.Join(root, "pty", id) }

// SocketPath returns the live-socket path for id under root.
func SocketPath(root, id string) string { return filepath.Join(Dir(root, id), SocketFile) }

// Meta describes a holder as recorded on disk.
type Meta struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Dir     string `json:"dir"`
	Command string `json:"command,omitempty"`
	PID     int    `json:"pid"`
	Started string `json:"started_at"`
	// Size is the terminal's current size as "<cols>x<rows>". The holder owns
	// the real PTY, so it is the only thing that can report this; without it
	// callers could request a resize but never read one back.
	Size string `json:"size,omitempty"`
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	return os.Rename(tmp, path)
}
