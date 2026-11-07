package transcript

// GrokOptions configures the Grok normaliser.
type GrokOptions struct {
	// SessionDir is the session folder, ~/.grok/sessions/<cwd>/<id>. It
	// holds subagents/<id>/meta.json, and child sessions are its siblings.
	// Empty skips both lookups; sub-agents still come from the parent lines.
	SessionDir string
}

// ProcessPather is implemented by normalisers whose processes write to a
// file. ProcessPath returns the output path the transcript named for a
// process id. The path is untrusted: callers confine it before reading.
type ProcessPather interface {
	ProcessPath(id string) (path string, ok bool)
}

// NewGrok returns the normaliser for Grok updates.jsonl files.
func NewGrok() Normaliser {
	return NewGrokWith(GrokOptions{})
}

// NewGrokWith returns the Grok normaliser with options.
func NewGrokWith(opts GrokOptions) Normaliser {
	return &grokNormaliser{opts: opts}
}

type grokNormaliser struct {
	opts GrokOptions
}

func (g *grokNormaliser) Kind() string { return "grok" }

func (g *grokNormaliser) Capabilities() Capabilities { return Capabilities{} }

func (g *grokNormaliser) PositionKind() string { return "" }

func (g *grokNormaliser) Feed(pos int64, line []byte) []Delta { return nil }

func (g *grokNormaliser) Flush() []Delta { return nil }

func (g *grokNormaliser) Reset() {}

func (g *grokNormaliser) ProcessPath(id string) (string, bool) { return "", false }
