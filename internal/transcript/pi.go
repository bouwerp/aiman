package transcript

// piNormaliser reads Pi session lines (docs/specs/phone-transcript.md,
// vendor table and rules 3, 5, 6, 7). Positions are byte offsets. Entries
// form an id/parentId tree; only the active branch is listed.
type piNormaliser struct{}

// NewPi returns the Pi session normaliser.
func NewPi() Normaliser { return &piNormaliser{} }

func (n *piNormaliser) Kind() string                        { return "pi" }
func (n *piNormaliser) Capabilities() Capabilities          { return Capabilities{} }
func (n *piNormaliser) PositionKind() string                { return "" }
func (n *piNormaliser) Feed(pos int64, line []byte) []Delta { return nil }
func (n *piNormaliser) Flush() []Delta                      { return nil }
func (n *piNormaliser) Reset()                              {}
