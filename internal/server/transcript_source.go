package server

import (
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/transcript"
)

// NewTranscriptSource returns the TranscriptSource cmd/aiman/serve.go passes
// to SetTranscripts. It finds a session's transcript with transcript.Locate
// under home, by the base agent command, AgentSessionID, AgentSessionPath and
// WorkingDirectory, and opens it with that agent's normaliser
// (docs/specs/phone-transcript.md, Structure and Behaviour rules 9 and 10).
//
// Until P5 builds it, every session has no transcript and the RPCs answer
// with the pane.
func NewTranscriptSource(home string) TranscriptSource {
	return noTranscripts{}
}

type noTranscripts struct{}

func (noTranscripts) Open(domain.Session, string) (TranscriptReader, error) {
	return nil, transcript.ErrNoTranscript
}
