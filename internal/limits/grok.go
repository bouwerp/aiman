package limits

// GrokScreen is what a rendered Grok screen is showing.
type GrokScreen int

const (
	// GrokOther is any screen no key may be sent to.
	GrokOther GrokScreen = iota
	// GrokTrust is the trust prompt: Enter, once.
	GrokTrust
	// GrokComposer is an empty composer with no dialog: /usage, once.
	GrokComposer
	// GrokUsage is the usage panel: read it.
	GrokUsage
)

// ClassifyGrokScreen classifies a rendered screen, with or without SGR codes.
// Stub until P3.
func ClassifyGrokScreen(_ string) GrokScreen { return GrokOther }

// ParseGrokUsage reads the usage panel. It returns false unless the plan label,
// the percentage and the reset line are all present. Stub until P3.
func ParseGrokUsage(_ string) (Reading, bool) { return Reading{}, false }

// NewGrokSource returns the probe source. Stub until P3.
func NewGrokSource(_ Env) Source { return stubSource{"grok"} }
