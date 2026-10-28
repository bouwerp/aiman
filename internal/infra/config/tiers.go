package config

// Model tiers. agents.smart is the smart tier and agents.preferred is the
// worker tier; the names are fixed.
const (
	TierSmart  = "smart"
	TierWorker = "worker"
)

// ValidTier reports whether name is one of the fixed tier names.
func ValidTier(name string) bool { return name == TierSmart || name == TierWorker }

// Tier returns the entries of the named tier: Smart for smart, Preferred for
// worker, nil for any other name.
func (a AgentsConfig) Tier(name string) []PreferredAgent {
	switch name {
	case TierSmart:
		return a.Smart
	case TierWorker:
		return a.Preferred
	default:
		return nil
	}
}

// SetTier replaces the entries of the named tier. Other names are ignored.
func (a *AgentsConfig) SetTier(name string, e []PreferredAgent) {
	switch name {
	case TierSmart:
		a.Smart = e
	case TierWorker:
		a.Preferred = e
	}
}
