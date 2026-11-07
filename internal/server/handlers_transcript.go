package server

import (
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/transcript"
)

// CodeForbidden refuses session.process_output when the path the transcript
// names resolves outside the located session's own directory.
const CodeForbidden = "forbidden"

// Transcript reply limits (docs/specs/phone-transcript.md, Behaviour rules 8
// and 10).
const (
	// TranscriptMaxReplyBytes caps the JSON encoding of one session.transcript
	// result; a larger page is shortened and keeps before honest.
	TranscriptMaxReplyBytes = 2 << 20
	// TranscriptItemMaxFieldBytes caps text, input and result in a
	// session.transcript_item reply, and Codex inline process output.
	TranscriptItemMaxFieldBytes = 1 << 20
	// ProcessOutputMaxBytes caps the data of one session.process_output reply.
	ProcessOutputMaxBytes = 256 << 10
	// TranscriptEventDebounce is the shortest gap between two transcript
	// events for one session and file.
	TranscriptEventDebounce = time.Second
)

// TranscriptSource opens a session's transcript. subagent is empty for the
// parent conversation. An error matching transcript.ErrNoTranscript means the
// session has none, and session.transcript answers with the pane.
type TranscriptSource interface {
	Open(sess domain.Session, subagent string) (TranscriptReader, error)
}

// TranscriptReader is one opened transcript, parent or sub-agent.
//
// Page and After follow transcript.Index. Item returns one item by id at full
// size (a process item is keyed by its process id); the handler applies the
// 1 MiB field cap. ProcessPath returns the output file the transcript names
// for a process id, false when the process has no file (Codex inline output
// is then the process item's Text). SessionDirs are the located session's own
// directories that a process output path must resolve inside. Files are the
// files the event poll stats for this reader.
type TranscriptReader interface {
	Page(before *transcript.Cursor, limit int) (transcript.Page, error)
	After(c transcript.Cursor, limit int) (transcript.Page, error)
	Item(id string) (transcript.Item, bool)
	ProcessPath(id string) (string, bool)
	SessionDirs() []string
	Files() []string
}

// SetTranscripts sets where session.transcript, session.transcript_item,
// session.process_output and the transcript event read from. With none set
// the three methods answer unavailable.
func (s *Server) SetTranscripts(src TranscriptSource) {
	s.transcripts = src
}
