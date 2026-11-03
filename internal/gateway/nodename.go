package gateway

import "strings"

const (
	nodeNamePrefix = "aiman-gateway"
	// maxNodeName is the DNS label limit; MagicDNS names are single labels.
	maxNodeName = 63
)

// NodeName is the default tsnet hostname for a gateway on host: the prefix
// plus the OS hostname reduced to [a-z0-9-], so several remotes on one
// tailnet do not collide. An empty or all-symbol host gives the bare prefix.
func NodeName(host string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(host) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	label := strings.Trim(b.String(), "-")
	if label == "" {
		return nodeNamePrefix
	}
	name := nodeNamePrefix + "-" + label
	if len(name) > maxNodeName {
		name = strings.TrimRight(name[:maxNodeName], "-")
	}
	return name
}
