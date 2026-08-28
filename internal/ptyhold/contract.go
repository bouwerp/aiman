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
//	activity.json holder's rolling view of the live session: when output last
//	              arrived, and the terminal title the child last set. Cheap to
//	              read, so callers can judge what a session is doing without
//	              replaying its output through an emulator.
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
	// HolderLogFile captures the holder process's own stdout/stderr, so a
	// holder that dies before it can write the exit file still says why.
	HolderLogFile = "holder.log"
	SpoolFile     = "spool"
	SpoolOld      = "spool.old"
	SocketFile    = "term.sock"
	ResizeFile    = "resize"
	KillFile      = "kill"
	ExitFile      = "exit"
	ActivityFile  = "activity.json"
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

// Activity is the holder's rolling view of a live session, refreshed as output
// arrives. Field names are part of the durable contract: never rename or
// repurpose, only ever add.
//
// This exists because the only signals available about a PTY session used to be
// its rendered screen — which meant replaying the whole spool through an
// emulator and then pattern-matching the result. The holder already sees every
// byte, so the cheap, certain facts are recorded here instead: tmux offers the
// same thing through #{session_activity}, and PTY sessions had no equivalent.
type Activity struct {
	// LastOutput is when the child last produced output, RFC3339. The direct
	// equivalent of tmux's #{session_activity}.
	LastOutput string `json:"last_output,omitempty"`
	// Bytes is how much output the session has produced in total.
	Bytes int64 `json:"bytes,omitempty"`
	// Title is the terminal title the child last set (OSC 0/2). Agents put
	// their current activity here — Claude Code sets "<spinner> <task>" and
	// changes it several times a second while it works.
	Title string `json:"title,omitempty"`
	// TitleChanged is when Title last changed, RFC3339. A title that is still
	// moving is the most direct evidence there is that an agent is working, and
	// unlike a rendered spinner it needs no pattern matching.
	TitleChanged string `json:"title_changed_at,omitempty"`
	// AltScreen and Mouse are the terminal modes the agent turned on for itself.
	// Attach mirrors them rather than asserting a fixed set, because a client
	// that reattaches mid-session never sees the setup the agent emitted on its
	// first frame.
	AltScreen bool `json:"alt_screen,omitempty"`
	Mouse     bool `json:"mouse,omitempty"`
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	return os.Rename(tmp, path)
}
